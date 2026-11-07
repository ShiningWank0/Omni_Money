"""CI-only AT-SPI operations against the unmodified packaged Wails window.

No JS injection, HTTP test endpoint, mocked bindings, screenshots or vault dumps.
Reports contain fixed UI labels and structural diagnostics, never arbitrary text
or password/recovery-code values.
"""

import json
import os
from pathlib import Path
import subprocess
import sys
import time


def require_ci(platform, env):
    if platform != "linux" or env.get("GITHUB_ACTIONS") != "true":
        raise RuntimeError("Native UI automation must not run locally")
    if not all(env.get(key) for key in ("RUNNER_TEMP", "DISPLAY", "DBUS_SESSION_BUS_ADDRESS")):
        raise RuntimeError("A private CI display and D-Bus session are required")


def owned_window_id(listing, pid):
    matches = []
    for line in listing.splitlines():
        fields = line.split(None, 4)
        if len(fields) == 5 and fields[2] == str(pid) and fields[4] == "Omni Money":
            matches.append(fields[0])
    if len(matches) != 1:
        raise RuntimeError("Expected exactly one native window belonging to this test PID")
    return matches[0]


def ledger_ready(items, api, record):
    """Read static text through AT-SPI Text, without recording arbitrary UI data."""
    expected = "現在の残高"
    text_roles = {api.Role.STATIC, api.Role.TEXT, api.Role.LABEL, api.Role.PARAGRAPH,
                  api.Role.SECTION, api.Role.PANEL}
    probe = {"expected_text": expected, "text_nodes": 0, "name_matches": 0,
             "text_matches": 0, "visible_text_matches": 0, "visible_matches": 0, "visible_alerts": 0,
             "candidates": []}
    for item in items:
        role = item.get_role()
        states = item.get_state_set()
        visible = states.contains(api.StateType.VISIBLE)
        showing = states.contains(api.StateType.SHOWING)
        if role == api.Role.ALERT and visible and showing:
            probe["visible_alerts"] += 1
        if role not in text_roles or states.contains(api.StateType.EDITABLE):
            continue
        name_matches = item.get_name() == expected
        text_matches = False
        text = item.get_text_iface()
        if text is not None:
            probe["text_nodes"] += 1
            count = api.Text.get_character_count(text)
            # Avoid reading large containers. Never inspect password/entry values.
            if 0 < count <= 128:
                # get_text_iface() returns an Accessible implementing Text.
                # Its deprecated get_text() getter shadows Text.get_text().
                text_matches = api.Text.get_text(text, 0, count).strip() == expected
        probe["name_matches"] += int(name_matches)
        probe["text_matches"] += int(text_matches)
        probe["visible_text_matches"] += int(text_matches and visible and showing)
        # AT-SPI STATIC objects may expose their displayed text only as a name.
        # When a Text interface exists, its actual body takes precedence.
        static_name_match = role == api.Role.STATIC and text is None and name_matches
        probe["visible_matches"] += int((text_matches or static_name_match) and visible and showing)
        if (name_matches or text_matches) and len(probe["candidates"]) < 8:
            probe["candidates"].append({"role": int(role), "name_matches": name_matches,
                                        "text_available": text is not None,
                                        "matched_text": expected if text_matches else None,
                                        "visible": visible, "showing": showing})
    record(probe)
    if probe["visible_alerts"]:
        raise RuntimeError("The real native application displayed an error alert while loading the ledger")
    return probe["visible_matches"] > 0


def control_ready(items, name, role, api, record):
    """Scroll an existing visible control into view before allowing an action."""
    matches = [item for item in items if item.get_role() == role and item.get_name() == name]
    visible = [item for item in matches if item.get_state_set().contains(api.StateType.VISIBLE)]
    probe = {"matching_nodes": len(matches), "visible_nodes": len(visible),
             "scroll_requested": False, "scroll_succeeded": False, "showing": False}
    if len(visible) > 1:
        record(probe)
        raise RuntimeError("Ambiguous native UI control")
    item = visible[0] if visible else None
    if item is not None:
        if not item.get_state_set().contains(api.StateType.SHOWING):
            component = item.get_component_iface()
            if component is not None:
                probe["scroll_requested"] = True
                probe["scroll_succeeded"] = bool(api.Component.scroll_to(component, api.ScrollType.ANYWHERE))
        states = item.get_state_set()
        probe["showing"] = states.contains(api.StateType.VISIBLE) and states.contains(api.StateType.SHOWING)
    record(probe)
    return item if probe["showing"] else None


def run(pid, scenario, report):
    require_ci(sys.platform, os.environ)
    if pid <= 1 or scenario not in ("setup", "reopen"):
        raise RuntimeError("Invalid owned process or UI scenario")
    password = os.environ["OMNI_WAILS_TEST_PASSWORD"]
    import gi
    gi.require_version("Atspi", "2.0")
    from gi.repository import Atspi, GLib

    Atspi.init()
    Atspi.set_timeout(2000, 5000)
    desktop = Atspi.get_desktop(0)
    steps = []
    diagnostics = {}
    phase = "finding owned Wails application"

    def save(passed=False, error=None):
        report.write_text(json.dumps({"passed": passed, "scenario": scenario, "phase": phase,
                                      "steps": steps, "diagnostics": diagnostics,
                                      "error": error}, ensure_ascii=False, indent=2) + "\n")

    def record_ledger_probe(probe):
        diagnostics["ledger"] = probe
        save()

    def record_control_probe(name, probe):
        # Names come only from this script's fixed UI labels, never input values.
        controls = diagnostics.setdefault("controls", {})
        previous = controls.get(name, {})
        controls[name] = {**probe,
                          "scroll_attempts": previous.get("scroll_attempts", 0) + int(probe["scroll_requested"]),
                          "scroll_successes": previous.get("scroll_successes", 0) + int(probe["scroll_succeeded"])}
        save()

    def pump():
        context = GLib.MainContext.default()
        while context.pending():
            context.iteration(False)

    def wait(check, description, timeout=20):
        nonlocal phase
        phase = description
        save()
        deadline = time.monotonic() + timeout
        last_error = None
        while time.monotonic() < deadline:
            pump()
            try:
                result = check()
                if result:
                    return result
            except GLib.Error as error:
                # A Vue update can invalidate a previously returned accessible.
                last_error = error
            time.sleep(0.1)
        raise RuntimeError(f"Timed out: {description}" + (f" ({last_error.domain})" if last_error else ""))

    def find_app():
        for index in range(desktop.get_child_count()):
            candidate = desktop.get_child_at_index(index)
            if candidate and candidate.get_process_id() == pid:
                return candidate
        return None

    app = None

    def nodes(parent):
        # Never traverse another application's accessibility tree.
        queue = [parent]
        visited = 0
        while queue:
            item = queue.pop()
            if item is None:
                continue
            visited += 1
            if visited > 5000:
                raise RuntimeError("Unexpectedly large owned accessibility tree")
            yield item
            for index in reversed(range(item.get_child_count())):
                queue.append(item.get_child_at_index(index))

    def showing(item):
        states = item.get_state_set()
        return states.contains(Atspi.StateType.VISIBLE) and states.contains(Atspi.StateType.SHOWING)

    def matches(name=None, role=None, parent=None):
        return [item for item in nodes(parent or app) if showing(item)
                and (name is None or item.get_name() == name)
                and (role is None or item.get_role() == role)]

    def unique(name=None, role=None, parent=None):
        found = matches(name, role, parent)
        if len(found) > 1:
            raise RuntimeError("Ambiguous native UI control")
        return found[0] if found else None

    def control(name, role=Atspi.Role.PUSH_BUTTON):
        return wait(lambda: control_ready(nodes(app), name, role, Atspi,
                                         lambda probe: record_control_probe(name, probe)), f"finding {name}")

    def enabled(item):
        return item.get_state_set().contains(Atspi.StateType.ENABLED)

    def activate(item):
        if not enabled(item):
            raise RuntimeError("The native UI control is disabled")
        action = item.get_action_iface()
        if not action or action.get_n_actions() < 1 or not action.do_action(0):
            raise RuntimeError("The native UI action was rejected")

    def click(name):
        activate(control(name))

    def fill(name):
        item = control(name, Atspi.Role.PASSWORD_TEXT)
        if not item.get_component_iface().grab_focus():
            raise RuntimeError("Could not focus the native password field")
        # Real keyboard events update Vue's v-model; never set a DOM property.
        subprocess.run(["xdotool", "key", "--clearmodifiers", "ctrl+a"], check=True, timeout=5)
        subprocess.run(["xdotool", "type", "--clearmodifiers", "--file", "-"], input=password,
                       text=True, check=True, timeout=5)

    def no_alerts():
        if matches(role=Atspi.Role.ALERT):
            raise RuntimeError("The real native application displayed an error alert")

    def licenses(label):
        click("オープンソースライセンス")
        dialog = wait(lambda: unique(role=Atspi.Role.DIALOG), "opening native license dialog")
        control("ライセンスを閉じる")
        wait(lambda: bool(matches("Omni Money — Apache-2.0", parent=dialog)), "reading native license catalog")
        click("ライセンスを閉じる")
        wait(lambda: not matches(role=Atspi.Role.DIALOG), "closing native license dialog")
        no_alerts()
        steps.append(label)

    def ledger():
        control("メニューを開く")
        wait(lambda: ledger_ready(nodes(app), Atspi, record_ledger_probe), "loading the real ledger")
        no_alerts()

    try:
        app = wait(find_app, phase)
        if scenario == "setup":
            control("管理者と保管庫を作成")
            no_alerts()
            licenses("OSS dialog before setup")
            fill("パスワード（UTF-8で12 bytes以上）")
            fill("パスワードの確認")
            click("管理者と保管庫を作成")
            control("回復コードをコピー")
            open_vault = control("保管庫を開く")
            if enabled(open_vault):
                raise RuntimeError("Recovery acknowledgement was bypassed")
            saved = control("パスワード管理ツール等、Omni Moneyとは別の安全な場所に保存しました", Atspi.Role.CHECK_BOX)
            activate(saved)
            wait(lambda: (button := unique("保管庫を開く", Atspi.Role.PUSH_BUTTON)) and enabled(button), "acknowledging test recovery code")
            click("保管庫を開く")
            ledger()
            steps.append("real Go vault setup and recovery acknowledgement")
            click("メニューを開く")
            licenses("OSS dialog in application menu")
            click("保管庫をロック")
            control("ロックを解除")
            no_alerts()
            steps.append("lock through real Go binding")
        else:
            control("ロックを解除")
            if matches("管理者と保管庫を作成", Atspi.Role.PUSH_BUTTON):
                raise RuntimeError("The configured test vault did not persist across restart")
            no_alerts()
            steps.append("persisted vault starts locked after restart")
        licenses("OSS dialog while locked")
        fill("パスワード")
        click("ロックを解除")
        ledger()
        steps.append("unlock through real Go binding")
        listing = subprocess.run(["wmctrl", "-lp"], check=True, capture_output=True, text=True, timeout=5).stdout
        window = owned_window_id(listing, pid)
        subprocess.run(["wmctrl", "-i", "-c", window], check=True, timeout=5)
        steps.append("requested normal close of owned native window")
        phase = "completed"
        save(passed=True)
    except Exception as error:
        # Do not dump the tree: it can contain the generated recovery code.
        save(error=str(error))
        raise
    finally:
        Atspi.exit()


if __name__ == "__main__":
    require_ci(sys.platform, os.environ)
    if len(sys.argv) != 4:
        raise SystemExit("Usage: wails-native-ui.py OWNED_PID setup|reopen REPORT_PATH")
    run(int(sys.argv[1]), sys.argv[2], Path(sys.argv[3]))
