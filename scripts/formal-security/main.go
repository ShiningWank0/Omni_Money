// Command formal-security proves contracts for the ledger's signed 64-bit arithmetic.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const zero = "#x0000000000000000"

type contract struct {
	name string
	op   string // Empty for the transaction amount predicate.
}

var contracts = []contract{
	{"ValidateTransactionAmount", ""},
	{"CheckedAddInt64", "bvadd"},
	{"CheckedSubInt64", "bvsub"},
}

type source struct {
	positions *token.FileSet
	info      *types.Info
	functions map[string]*ast.FuncDecl
}

func loadSource(path string) (*source, error) {
	inBuild, err := build.Default.MatchFile(filepath.Dir(path), filepath.Base(path))
	if err != nil || !inBuild {
		return nil, fmt.Errorf("source is unavailable in this build: %s (%v)", path, err)
	}
	pos := token.NewFileSet()
	file, err := parser.ParseFile(pos, path, nil, 0)
	if err != nil {
		return nil, err
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	config := types.Config{Importer: importer.Default()}
	if _, err := config.Check("omni_money/backend/validation", pos, []*ast.File{file}, info); err != nil {
		return nil, err
	}
	s := &source{pos, info, map[string]*ast.FuncDecl{}}
	for _, decl := range file.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok && f.Recv == nil {
			s.functions[f.Name.Name] = f
		}
	}
	return s, nil
}

type encoder struct {
	*source
	params map[types.Object]string
}

func (e encoder) unsupported(node ast.Node) error {
	return fmt.Errorf("%s: unsupported proof input (%T)", e.positions.Position(node.Pos()), node)
}

var operators = map[token.Token]string{
	token.ADD: "bvadd", token.SUB: "bvsub", token.LSS: "bvslt", token.LEQ: "bvsle",
	token.GTR: "bvsgt", token.GEQ: "bvsge", token.EQL: "=", token.NEQ: "distinct",
	token.LAND: "and", token.LOR: "or",
}

func (e encoder) expression(node ast.Expr) (string, error) {
	if v := e.info.Types[node].Value; v != nil {
		switch v.Kind() {
		case constant.Int:
			if n, ok := constant.Int64Val(v); ok {
				if n < 0 {
					return fmt.Sprintf("(bvnot #x%016x)", ^n), nil
				}
				return fmt.Sprintf("#x%016x", n), nil
			}
		case constant.Bool:
			return fmt.Sprint(constant.BoolVal(v)), nil
		}
	}
	switch n := node.(type) {
	case *ast.Ident:
		if name, ok := e.params[e.info.Uses[n]]; ok {
			return name, nil
		}
	case *ast.ParenExpr:
		return e.expression(n.X)
	case *ast.UnaryExpr:
		if n.Op == token.NOT {
			x, err := e.expression(n.X)
			return "(not " + x + ")", err
		}
	case *ast.BinaryExpr:
		if op, ok := operators[n.Op]; ok {
			x, err := e.expression(n.X)
			if err != nil {
				return "", err
			}
			y, err := e.expression(n.Y)
			return fmt.Sprintf("(%s %s %s)", op, x, y), err
		}
	}
	return "", e.unsupported(node)
}

type outcome struct{ accepted, value string }

func (e encoder) returned(node *ast.ReturnStmt, arity int) (outcome, error) {
	if len(node.Results) != arity {
		return outcome{}, e.unsupported(node)
	}
	out := outcome{"false", zero}
	if arity == 2 {
		value, err := e.expression(node.Results[0])
		if err != nil {
			return outcome{}, err
		}
		out.value = value
	}
	last := node.Results[arity-1]
	if id, ok := last.(*ast.Ident); ok && e.info.Uses[id] == types.Universe.Lookup("nil") {
		out.accepted = "true"
		return out, nil
	}
	// Only fmt.Errorf with a literal format and side-effect-free scalar arguments
	// is modeled as a non-nil error. Other calls must receive their own semantics.
	if call, ok := last.(*ast.CallExpr); ok && len(call.Args) > 0 && !call.Ellipsis.IsValid() {
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			f, ok := e.info.Uses[sel.Sel].(*types.Func)
			if ok && f.Pkg().Path() == "fmt" && f.Name() == "Errorf" {
				format, ok := call.Args[0].(*ast.BasicLit)
				if !ok || format.Kind != token.STRING {
					return outcome{}, e.unsupported(last)
				}
				for _, arg := range call.Args[1:] {
					if _, err := e.expression(arg); err != nil {
						return outcome{}, err
					}
				}
				return out, nil
			}
		}
	}
	return outcome{}, e.unsupported(last)
}

// Encode the entire supported function body, preserving the order of early returns.
func (e encoder) body(block *ast.BlockStmt, arity int) (outcome, error) {
	if block == nil || len(block.List) == 0 {
		return outcome{}, fmt.Errorf("missing function body")
	}
	last := len(block.List) - 1
	ret, ok := block.List[last].(*ast.ReturnStmt)
	if !ok {
		return outcome{}, e.unsupported(block.List[last])
	}
	out, err := e.returned(ret, arity)
	if err != nil {
		return outcome{}, err
	}
	for i := last - 1; i >= 0; i-- {
		guard, ok := block.List[i].(*ast.IfStmt)
		if !ok || guard.Init != nil || guard.Else != nil || len(guard.Body.List) != 1 {
			return outcome{}, e.unsupported(block.List[i])
		}
		early, ok := guard.Body.List[0].(*ast.ReturnStmt)
		if !ok {
			return outcome{}, e.unsupported(guard.Body.List[0])
		}
		cond, err := e.expression(guard.Cond)
		if err != nil {
			return outcome{}, err
		}
		yes, err := e.returned(early, arity)
		if err != nil {
			return outcome{}, err
		}
		out = outcome{fmt.Sprintf("(ite %s %s %s)", cond, yes.accepted, out.accepted), fmt.Sprintf("(ite %s %s %s)", cond, yes.value, out.value)}
	}
	return out, nil
}

func (s *source) query(c contract) (string, error) {
	f := s.functions[c.name]
	if f == nil {
		return "", fmt.Errorf("missing production function %s", c.name)
	}
	sig := s.info.Defs[f.Name].Type().(*types.Signature)
	count := 1
	if c.op != "" {
		count = 2
	}
	if sig.Variadic() || sig.TypeParams().Len() != 0 || sig.Params().Len() != count || sig.Results().Len() != count || !types.Identical(sig.Results().At(count-1).Type(), types.Universe.Lookup("error").Type()) {
		return "", fmt.Errorf("%s: unexpected function signature", c.name)
	}
	if count == 2 && !types.Identical(sig.Results().At(0).Type(), types.Typ[types.Int64]) {
		return "", fmt.Errorf("%s: expected int64 result", c.name)
	}
	e := encoder{s, map[types.Object]string{}}
	var q strings.Builder
	q.WriteString("(set-logic QF_BV)\n(set-option :timeout 15000)\n")
	for i := 0; i < count; i++ {
		param := sig.Params().At(i)
		if !types.Identical(param.Type(), types.Typ[types.Int64]) {
			return "", fmt.Errorf("%s: expected int64 parameter", c.name)
		}
		name := fmt.Sprintf("x%d", i)
		e.params[param] = name
		fmt.Fprintf(&q, "(declare-const %s (_ BitVec 64))\n", name)
	}
	out, err := e.body(f.Body, count)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&q, "(define-fun accepted () Bool %s)\n", out.accepted)
	if count == 1 {
		// Independent product rule: 1 through one billion yen, inclusive.
		q.WriteString("(assert (distinct accepted (and (bvsgt x0 #x0000000000000000) (bvsle x0 #x000000003b9aca00))))\n")
	} else {
		// Two signed 64-bit operands always have an exact 65-bit sum/difference.
		fmt.Fprintf(&q, "(define-fun exact () (_ BitVec 65) (%s ((_ sign_extend 1) x0) ((_ sign_extend 1) x1)))\n", c.op)
		q.WriteString("(define-fun fits () Bool (= exact ((_ sign_extend 1) ((_ extract 63 0) exact))))\n")
		fmt.Fprintf(&q, "(assert (or (distinct accepted fits) (and accepted (distinct ((_ sign_extend 1) %s) exact))))\n", out.value)
	}
	q.WriteString("(check-sat)\n")
	return q.String(), nil
}

var errViolated = errors.New("the implementation and specification disagree on acceptance or result")

func prove(bin, query string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-in", "-smt2")
	cmd.Stdin = strings.NewReader(query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("solver failed: %w (%s)", err, strings.TrimSpace(string(output)))
	}
	switch result := strings.TrimSpace(string(output)); result {
	case "unsat":
		return nil
	case "sat":
		return errViolated
	default:
		return fmt.Errorf("proof incomplete: solver returned %q", result)
	}
}

const sourcePath = "backend/validation/amount.go"

type proofRecord struct {
	Function string `json:"function"`
	Status   string `json:"status"`
	Query    string `json:"smtlib,omitempty"`
}

type report struct {
	Schema   int                 `json:"schema"`
	Scan     string              `json:"scan"`
	Status   string              `json:"status"`
	Findings []map[string]string `json:"findings"`
	Proofs   []proofRecord       `json:"proofs,omitempty"`
}

func verify(s *source, bin string) (report, error) {
	r := report{Schema: 1, Scan: "formal-ledger", Status: "clean", Findings: []map[string]string{}}
	var failures []error
	for _, c := range contracts {
		q, err := s.query(c)
		record := proofRecord{Function: c.name, Status: "error", Query: q}
		if err == nil {
			err = prove(bin, q)
		}
		if err == nil {
			record.Status = "unsat"
			r.Proofs = append(r.Proofs, record)
			fmt.Printf("PROVED %s (%s)\n", c.name, s.positions.Position(s.functions[c.name].Pos()))
			continue
		}
		failures = append(failures, fmt.Errorf("%s / %s: %w", sourcePath, c.name, err))
		if !errors.Is(err, errViolated) {
			r.Status = "error"
			r.Proofs = append(r.Proofs, record)
			continue
		}
		record.Status = "sat"
		r.Proofs = append(r.Proofs, record)
		if r.Status == "clean" {
			r.Status = "findings"
		}
		r.Findings = append(r.Findings, map[string]string{
			"tool": "formal", "id": c.name, "component": sourcePath, "severity": "UNKNOWN", "installed": "", "fixed": "",
			"summary": "金額の受理条件または計算結果が台帳の仕様に一致しない入力が存在します。",
			"context": s.positions.Position(s.functions[c.name].Pos()).String(),
		})
	}
	return r, errors.Join(failures...)
}

func run() error {
	bin := flag.String("z3", "z3", "path to the Z3 executable")
	path := flag.String("report", "", "write the CI security report to this path")
	flag.Parse()
	s, err := loadSource(sourcePath)
	r := report{Schema: 1, Scan: "formal-ledger", Status: "error", Findings: []map[string]string{}}
	if err == nil {
		r, err = verify(s, *bin)
	}
	if *path != "" {
		if dirErr := os.MkdirAll(filepath.Dir(*path), 0700); dirErr != nil {
			return errors.Join(err, dirErr)
		}
		data, jerr := json.MarshalIndent(r, "", "  ")
		if jerr != nil {
			return errors.Join(err, jerr)
		}
		err = errors.Join(err, os.WriteFile(*path, append(data, '\n'), 0600))
	}
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
