"""Offline native UI query regressions; no real app, private data or AT-SPI bus."""

import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("native_ui", Path(__file__).with_name("wails-native-ui.py"))
ui = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ui)


class API:
    class Role:
        STATIC, TEXT, LABEL, PARAGRAPH, SECTION, PANEL, ALERT, PASSWORD_TEXT, ENTRY, PUSH_BUTTON = range(10)

    class StateType:
        VISIBLE, SHOWING, EDITABLE = range(3)


class States:
    def __init__(self, values):
        self.values = values

    def contains(self, value):
        return value in self.values


class Text:
    def __init__(self, value):
        self.value = value
        self.reads = 0

    def get_character_count(self):
        return len(self.value)

    def get_text(self, start, end):
        self.reads += 1
        return self.value[start:end]


class Node:
    def __init__(self, *, name="", text=None, role=API.Role.STATIC, visible=True, showing=True, editable=False):
        self.name, self.text, self.role = name, Text(text) if text is not None else None, role
        self.states = States({state for enabled, state in [
            (visible, API.StateType.VISIBLE), (showing, API.StateType.SHOWING),
            (editable, API.StateType.EDITABLE)] if enabled})

    def get_role(self):
        return self.role

    def get_state_set(self):
        return self.states

    def get_name(self):
        return self.name

    def get_text_iface(self):
        if self.role in (API.Role.PASSWORD_TEXT, API.Role.ENTRY) or self.states.contains(API.StateType.EDITABLE):
            raise AssertionError("Secret/entry values must not be inspected")
        return self.text


class LedgerTextTest(unittest.TestCase):
    def check(self, nodes):
        reports = []
        ready = ui.ledger_ready(nodes, API, reports.append)
        self.assertEqual(len(reports), 1)
        return ready, reports[0]

    def test_visible_text_with_empty_accessible_name_is_found(self):
        node = Node(text="現在の残高")
        ready, probe = self.check([node])
        self.assertTrue(ready)
        self.assertEqual(node.text.reads, 1)
        self.assertEqual(probe["name_matches"], 0)
        self.assertEqual(probe["visible_text_matches"], 1)
        self.assertEqual(probe["candidates"][0]["matched_text"], "現在の残高")

    def test_text_body_takes_precedence_over_a_descriptive_name(self):
        for node in (Node(name="現在の残高", role=API.Role.TEXT), Node(name="現在の残高", text="different text")):
            with self.subTest(node=node):
                ready, probe = self.check([node])
                self.assertFalse(ready)
                self.assertEqual(probe["name_matches"], 1)
                self.assertEqual(probe["text_matches"], 0)

    def test_static_name_only_representation_remains_supported(self):
        ready, probe = self.check([Node(name="現在の残高")])
        self.assertTrue(ready)
        self.assertFalse(probe["candidates"][0]["text_available"])
        self.assertEqual(probe["visible_matches"], 1)

    def test_readonly_html_containers_can_expose_the_label_through_text(self):
        for role in (API.Role.SECTION, API.Role.PANEL):
            with self.subTest(role=role):
                self.assertTrue(self.check([Node(text="現在の残高", role=role)])[0])

    def test_hidden_or_offscreen_text_is_not_accepted(self):
        for state in ({"visible": False}, {"showing": False}):
            with self.subTest(state=state):
                ready, probe = self.check([Node(text="現在の残高", **state)])
                self.assertFalse(ready)
                self.assertEqual(probe["text_matches"], 1)
                self.assertEqual(probe["visible_text_matches"], 0)

    def test_static_text_whitespace_is_allowed_but_a_substring_is_not(self):
        self.assertTrue(self.check([Node(text="\n現在の残高\n", role=API.Role.PARAGRAPH)])[0])
        self.assertFalse(self.check([Node(text="現在の残高を読み込み中")])[0])

    def test_diagnostics_exclude_unknown_names_text_and_entry_values(self):
        sentinel = "synthetic-private-value-never-log"
        nodes = [Node(name=sentinel, text=sentinel), Node(name="現在の残高", text=sentinel),
                 Node(text=sentinel, role=API.Role.PASSWORD_TEXT), Node(text=sentinel, role=API.Role.ENTRY),
                 Node(text=sentinel, editable=True)]
        ready, probe = self.check(nodes)
        self.assertFalse(ready)
        self.assertNotIn(sentinel, json.dumps(probe))
        self.assertTrue(all(node.text.reads == 0 for node in nodes[2:]))

    def test_long_containers_are_not_read_or_written_to_the_report(self):
        node = Node(text="synthetic-private-value" * 20)
        ready, probe = self.check([node])
        self.assertFalse(ready)
        self.assertEqual(node.text.reads, 0)
        self.assertEqual(probe["candidates"], [])

    def test_missing_text_and_a_button_with_the_same_label_do_not_pass(self):
        self.assertFalse(self.check([])[0])
        self.assertFalse(self.check([Node(name="現在の残高", text="現在の残高", role=API.Role.PUSH_BUTTON)])[0])

    def test_visible_application_alert_fails_even_if_the_balance_label_is_missing(self):
        for nodes in ([Node(role=API.Role.ALERT)], [Node(text="現在の残高"), Node(role=API.Role.ALERT)]):
            with self.subTest(nodes=nodes):
                reports = []
                with self.assertRaisesRegex(RuntimeError, "error alert while loading the ledger"):
                    ui.ledger_ready(nodes, API, reports.append)
                self.assertEqual(reports[0]["visible_alerts"], 1)

    def test_hidden_alert_does_not_mask_visible_content(self):
        self.assertTrue(self.check([Node(text="現在の残高"), Node(role=API.Role.ALERT, showing=False)])[0])


if __name__ == "__main__":
    unittest.main()
