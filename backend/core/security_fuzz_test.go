package core

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/csv"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"reflect"
	"strings"
	"testing"
)

// Small chunks exercise quote/CRLF boundaries independently of csv.Reader's
// internal buffer. No databases, decoded attacker images or files are opened.
type fuzzCSVChunks struct {
	input io.Reader
	size  int
}

func (r fuzzCSVChunks) Read(p []byte) (int, error) {
	if len(p) > r.size {
		p = p[:r.size]
	}
	return r.input.Read(p)
}

func FuzzCSVFieldLimitReader(f *testing.F) {
	for _, seed := range []string{"", "a,b\n", "a,\"x\r\ny\"\r\n", "\"a\"\"b\",c\n", "\"unterminated", strings.Repeat("x", 65) + "\n", strings.Repeat("a,", maxCSVRecordFields) + "a\n", strings.Repeat("x", 64) + "\r\n", "a,b\r"} {
		f.Add(seed, uint8(1))
	}
	f.Fuzz(func(t *testing.T, input string, chunk uint8) {
		if len(input) > 16*1024 {
			t.Skip()
		}
		parse := func(limit int) ([][]string, error) {
			guard := &csvFieldLimitReader{ctx: context.Background(), input: fuzzCSVChunks{strings.NewReader(input), int(chunk%32) + 1}, maxFieldBytes: limit, fieldStart: true}
			reader := csv.NewReader(guard)
			reader.FieldsPerRecord = -1
			return reader.ReadAll()
		}
		reference := csv.NewReader(strings.NewReader(input))
		reference.FieldsPerRecord = -1
		expected, referenceErr := reference.ReadAll()
		// With a nonrestrictive field limit, the guard must preserve every valid
		// record, including escaped quotes and normalized CRLF, across chunk sizes.
		actual, err := parse(len(input) + 1)
		withinFieldCount := true
		for _, record := range expected {
			if len(record) > maxCSVRecordFields {
				withinFieldCount = false
			}
		}
		if referenceErr == nil && withinFieldCount && (err != nil || !reflect.DeepEqual(actual, expected)) {
			t.Fatal("bounded reader changed valid CSV")
		}
		actual, err = parse(64)
		if err == nil {
			if referenceErr != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatal("guard accepted or altered invalid CSV")
			}
			for _, record := range actual {
				if len(record) > maxCSVRecordFields {
					t.Fatal("CSV record exceeded its field-count budget")
				}
				for _, field := range record {
					if len(field) > 64 {
						t.Fatal("decoded CSV field exceeded its byte budget")
					}
				}
			}
		}
	})
}

func FuzzImageContainer(f *testing.F) {
	im := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var pngData, jpegData, gifData bytes.Buffer
	if err := png.Encode(&pngData, im); err != nil {
		f.Fatal(err)
	}
	if err := jpeg.Encode(&jpegData, im, nil); err != nil {
		f.Fatal(err)
	}
	if err := gif.Encode(&gifData, im, nil); err != nil {
		f.Fatal(err)
	}
	// This is a structural WebP seed, not a claim that a container alone is a
	// decodable image. The production path separately checks its decoder.
	webpData := append([]byte("RIFF\x0c\x00\x00\x00WEBPVP8L"), 0, 0, 0, 0)
	for i, seed := range [][]byte{jpegData.Bytes(), pngData.Bytes(), gifData.Bytes(), webpData} {
		f.Add(seed, uint8(i))
	}
	f.Add([]byte{}, uint8(0))
	f.Add([]byte("<svg onload=alert(1) />"), uint8(1))
	f.Fuzz(func(t *testing.T, data []byte, format uint8) {
		if len(data) > 64*1024 {
			t.Skip()
		}
		mimeType := []string{"image/jpeg", "image/png", "image/gif", "image/webp"}[format%4]
		original := bytes.Clone(data)
		if err := validateImageContainer(mimeType, data); err == nil {
			// Every allowed format has an exact terminal/declared-length boundary.
			// Accepting an appended payload would permit image/polyglot smuggling.
			appended := append(bytes.Clone(data), []byte("<script>alert(1)</script>")...)
			if validateImageContainer(mimeType, appended) == nil {
				t.Fatal("image accepted a payload after its container boundary")
			}
			if len(data) == 0 || validateImageContainer(mimeType, data[:len(data)-1]) == nil {
				t.Fatal("image accepted a truncated terminal boundary")
			}
			if mimeType == "image/webp" && int64(binary.LittleEndian.Uint32(data[4:8]))+8 != int64(len(data)) {
				t.Fatal("WebP declared length mismatch")
			}
		}
		if !bytes.Equal(data, original) {
			t.Fatal("image validation modified its input")
		}
	})
}
