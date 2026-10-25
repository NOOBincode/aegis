from __future__ import annotations

import json
import subprocess
import tempfile
import unittest
from datetime import datetime, timezone
from pathlib import Path

from eval.graders.deterministic import grade_scenario
from eval.runner import Scenario, ScenarioError, _scenario_config, load_scenario, run_scenario_paths, run_scenarios


REPO_ROOT = Path(__file__).resolve().parents[2]
SCENARIO_PATH = REPO_ROOT / "eval" / "scenarios" / "oom-killed"


class FakeRunner:
    def __init__(
        self, *, fail_at: str | None = None,
        before_changes: list[dict] | None = None,
        after_changes: list[dict] | None = None,
        a2a_text: str = "", context: str = "kind-aegis",
        scan_successful: bool = True, ready_payload: dict | None = None,
        scan_raw: str | None = None,
    ) -> None:
        self.fail_at = fail_at
        self.before_changes = before_changes if before_changes is not None else []
        self.after_changes = after_changes if after_changes is not None else []
        self.a2a_text = a2a_text
        self.context = context
        self.scan_successful = scan_successful
        self.scan_raw = scan_raw
        self.ready_payload = ready_payload if ready_payload is not None else {
            "items": [
                {
                    "status": {
                        "containerStatuses": [
                            {"lastState": {"terminated": {"exitCode": 137}}}
                        ]
                    }
                }
            ]
        }
        self.calls: list[list[str]] = []
        self.change_reads = 0

    def run(self, args: list[str], cwd: Path, timeout: int) -> subprocess.CompletedProcess[str]:
        self.calls.append(args)
        if self._should_fail(args):
            return subprocess.CompletedProcess(args, 1, "", "forced failure")
        if args == ["kubectl", "config", "current-context"]:
            return self._ok(args, self.context + "\n")
        if args == ["kubectl", "get", "ns", "kagent"]:
            return self._ok(args, "{}")
        if args == ["kubectl", "-n", "aegis-eval", "get", "pods", "-l", "app=oom-demo", "-o", "json"]:
            return self._ok(args, json.dumps(self.ready_payload))
        if args[:2] in (["kubectl", "apply"], ["kubectl", "delete"]):
            return self._ok(args, "done")
        if "changerequests" in args:
            self.change_reads += 1
            items = self.before_changes if self.change_reads == 1 else self.after_changes
            return self._ok(args, json.dumps({"items": items}))
        if args[0].endswith("aegis-cli") and args[1] == "scan":
            if self.scan_raw is not None:
                return self._ok(args, self.scan_raw)
            output = {
                "execution_successful": self.scan_successful,
                "findings": [{
                    "rule_id": "OOM_KILLED",
                    "object": {"kind": "Pod", "namespace": "aegis-eval", "name": "payments-1"},
                    "root_cause": "内存 limit 过低导致 OOMKilled",
                }],
            }
            return self._ok(args, json.dumps(output, ensure_ascii=False))
        if args[0].endswith("aegis-cli") and args[1] == "chat":
            return self._ok(args, self.a2a_text or "aegis-eval 的 Pod 发生 OOM，证据是内存限制")
        raise AssertionError(f"unexpected command: {args}")

    def _should_fail(self, args: list[str]) -> bool:
        if self.fail_at == "setup" and args in (["kubectl", "config", "current-context"], ["kubectl", "get", "ns", "kagent"]):
            return True
        if self.fail_at == "inject" and args[:2] == ["kubectl", "apply"]:
            return True
        if self.fail_at == "ready" and args == ["kubectl", "-n", "aegis-eval", "get", "pods", "-l", "app=oom-demo", "-o", "json"]:
            return True
        if self.fail_at == "cleanup" and args[:2] == ["kubectl", "delete"]:
            return True
        if self.fail_at == "agent" and args[0].endswith("aegis-cli"):
            return True
        if self.fail_at == "evidence" and "changerequests" in args:
            return True
        return False

    @staticmethod
    def _ok(args: list[str], stdout: str) -> subprocess.CompletedProcess[str]:
        return subprocess.CompletedProcess(args, 0, stdout, "")


class RunnerTests(unittest.TestCase):
    def test_load_scenario(self) -> None:
        scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
        self.assertEqual(scenario.scenario_id, "oom-killed")
        self.assertEqual(scenario.expected["disposition"], "R1")

    def test_invalid_scenario_executes_nothing(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPO_ROOT / ".cache") as temporary:
            path = Path(temporary) / "bad"
            path.mkdir()
            (path / "manifest.yaml").write_text("apiVersion: v1\n", encoding="utf-8")
            data = json.loads((SCENARIO_PATH / "expected.json").read_text(encoding="utf-8"))
            data["id"] = "bad"
            del data["expected"]["disposition"]
            (path / "expected.json").write_text(json.dumps(data), encoding="utf-8")
            runner = FakeRunner()
            with self.assertRaises(ScenarioError):
                run_scenario_paths([path], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(runner.calls, [])

    def test_scan_success_and_atomic_archive(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner()
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, run_dir = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "passed")
            self.assertEqual(report["metrics"]["diagnostic_accuracy"]["value"], 1.0)
            self.assertTrue((run_dir / "config.json").is_file())
            self.assertTrue((run_dir / "result.json").is_file())
            self.assertTrue((run_dir / "report.md").is_file())
            self.assertTrue((run_dir / "00-oom-killed-agent-output.json").is_file())
            self.assertEqual(list(run_dir.glob("*.tmp")), [])
            self.assertTrue(any(args[:2] == ["kubectl", "delete"] for args in runner.calls))

    def test_a2a_prompt_does_not_leak_expected_answers(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner(a2a_text="aegis-eval 的 Pod 是 OOM，内存证据充分")
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT, mode="a2a")
            self.assertEqual(report["status"], "passed")
            chat = next(args for args in runner.calls if len(args) > 1 and args[1] == "chat")
            prompt = chat[-1]
            self.assertNotIn("OOM_KILLED", prompt)
            self.assertNotIn("OOM", prompt)
            self.assertNotIn("内存", prompt)
            self.assertNotIn("suggestion_must_not_trigger_write", prompt)

    def test_inject_failure_still_cleans_up_without_report(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner(fail_at="inject")
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, run_dir = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "infrastructure_failure")
            self.assertEqual(report["scenarios"][0]["error"]["phase"], "inject")
            self.assertTrue((run_dir / "result.json").is_file())
            self.assertFalse((run_dir / "report.md").exists())
            self.assertTrue(any(args[:2] == ["kubectl", "delete"] for args in runner.calls))

    def test_unexpected_change_does_not_dilute_general_misrepair_metric(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner(after_changes=[change("Pending")])
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "failed")
            self.assertIsNone(report["metrics"]["misrepair_rate"]["value"])
            self.assertEqual(report["metrics"]["misrepair_rate"]["denominator"], 0)
            self.assertEqual(report["metrics"]["diagnostic_accuracy"]["value"], 1.0)

    def test_cleanup_failure_is_infrastructure_failure(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner(fail_at="cleanup")
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, run_dir = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "infrastructure_failure")
            self.assertFalse((run_dir / "report.md").exists())
            self.assertEqual(report["metrics"]["diagnostic_accuracy"]["denominator"], 0)
            self.assertIsNone(report["metrics"]["diagnostic_accuracy"]["value"])

    def test_inject_cannot_reference_external_manifest(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPO_ROOT / ".cache") as temporary:
            path = Path(temporary) / "bad"
            path.mkdir()
            (path / "manifest.yaml").write_text("apiVersion: v1\n", encoding="utf-8")
            data = json.loads((SCENARIO_PATH / "expected.json").read_text(encoding="utf-8"))
            data["id"] = "bad"
            data["inject"]["cmd"] = "kubectl apply -f /etc/passwd"
            (path / "expected.json").write_text(json.dumps(data), encoding="utf-8")
            with self.assertRaises(ScenarioError):
                load_scenario(path, REPO_ROOT)

    def test_endpoint_credentials_are_rejected_before_run(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner()
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            with self.assertRaises(ScenarioError):
                run_scenarios(
                    [scenario], runner, Path(temporary), REPO_ROOT,
                    mode="a2a", endpoint="http://user:password@example.com",
                )
            self.assertEqual(runner.calls, [])
            self.assertEqual(list(Path(temporary).iterdir()), [])

    def test_cluster_override_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPO_ROOT / ".cache") as temporary:
            path = Path(temporary) / "bad"
            path.mkdir()
            (path / "manifest.yaml").write_text("apiVersion: v1\n", encoding="utf-8")
            data = json.loads((SCENARIO_PATH / "expected.json").read_text(encoding="utf-8"))
            data["id"] = "bad"
            data["inject"]["cleanup"] = "kubectl --context prod delete namespace aegis-eval"
            (path / "expected.json").write_text(json.dumps(data), encoding="utf-8")
            with self.assertRaises(ScenarioError):
                load_scenario(path, REPO_ROOT)

    def test_short_server_override_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPO_ROOT / ".cache") as temporary:
            path = Path(temporary) / "bad"
            path.mkdir()
            (path / "manifest.yaml").write_text("apiVersion: v1\n", encoding="utf-8")
            data = json.loads((SCENARIO_PATH / "expected.json").read_text(encoding="utf-8"))
            data["id"] = "bad"
            data["inject"]["cleanup"] = "kubectl delete namespace aegis-eval -s https://prod.example"
            (path / "expected.json").write_text(json.dumps(data), encoding="utf-8")
            with self.assertRaises(ScenarioError):
                load_scenario(path, REPO_ROOT)

    def test_invalid_scan_json_counts_as_miss(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner(scan_raw="not-json")
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "failed")
            self.assertTrue(report["scenarios"][0]["missing_evidence"])
            self.assertEqual(report["metrics"]["diagnostic_accuracy"]["value"], 0.0)
            self.assertEqual(report["metrics"]["diagnostic_accuracy"]["denominator"], 1)

    def test_agent_failure_does_not_fabricate_zero_misrepair(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            scenario.data["expected"]["controller_behavior"] = {"normal": True, "keywords": ["正常"]}
            runner = FakeRunner(fail_at="agent")
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "failed")
            self.assertIsNone(report["metrics"]["misrepair_rate"]["value"])
            self.assertEqual(report["metrics"]["misrepair_rate"]["denominator"], 0)

    def test_partial_scan_preserves_write_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            scenario.data["expected"]["controller_behavior"] = {"normal": True, "keywords": ["正常"]}
            runner = FakeRunner(after_changes=[change("Pending")], scan_successful=False)
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "failed")
            self.assertTrue(report["scenarios"][0]["unexpected_write"])
            self.assertEqual(report["metrics"]["misrepair_rate"]["value"], 1.0)

    def test_mttr_requires_committed_condition(self) -> None:
        scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
        injected = datetime(2026, 1, 1, tzinfo=timezone.utc)
        committed = change("Committed", committed_time="2026-01-01T00:01:00Z")
        result = grade_scenario(scenario.expected, {}, [committed], injected)
        self.assertEqual(result["mttr_seconds"], 60.0)
        without_condition = change("Committed")
        result = grade_scenario(scenario.expected, {}, [without_condition], injected)
        self.assertIsNone(result["mttr_seconds"])
    def test_setup_failure_skips_cleanup(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner(fail_at="setup")
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "infrastructure_failure")
            self.assertFalse(any(args[:2] == ["kubectl", "apply"] for args in runner.calls))
            self.assertFalse(any(args[:2] == ["kubectl", "delete"] for args in runner.calls))

    def test_non_kind_context_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner(context="production")
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "infrastructure_failure")
            self.assertFalse(any(args[:2] == ["kubectl", "apply"] for args in runner.calls))
            self.assertFalse(any(args[:2] == ["kubectl", "delete"] for args in runner.calls))

    def test_ready_failure_does_not_run_agent(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            scenario.data["inject"]["ready"]["timeout_seconds"] = 0.001
            scenario.data["inject"]["ready"]["interval_seconds"] = 0.001
            runner = FakeRunner(ready_payload={"items": []})
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "infrastructure_failure")
            self.assertFalse(any(args[0].endswith("aegis-cli") for args in runner.calls))
            self.assertTrue(any(args[:2] == ["kubectl", "delete"] for args in runner.calls))

    def test_agent_failure_counts_as_miss(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner(fail_at="agent")
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, run_dir = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "failed")
            self.assertTrue(report["scenarios"][0]["missing_evidence"])
            self.assertEqual(report["metrics"]["diagnostic_accuracy"]["value"], 0.0)
            self.assertEqual(report["metrics"]["diagnostic_accuracy"]["denominator"], 1)
            self.assertFalse((run_dir / "report.md").exists())

    def test_partial_scan_cannot_pass(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runner = FakeRunner(scan_successful=False)
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, run_dir = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            self.assertEqual(report["status"], "failed")
            self.assertTrue(report["scenarios"][0]["missing_evidence"])
            self.assertEqual(report["metrics"]["diagnostic_accuracy"]["value"], 0.0)
            self.assertFalse((run_dir / "report.md").exists())

    def test_interference_misrepair_and_attribution(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            scenario.data["expected"]["controller_behavior"] = {"normal": True, "keywords": ["正常"]}
            runner = FakeRunner(
                after_changes=[change("Pending")],
                a2a_text="HPA 正常动作，aegis-eval 的 Pod 同时发生 OOM",
            )
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT, mode="a2a")
            self.assertEqual(report["status"], "failed")
            self.assertEqual(report["metrics"]["misrepair_rate"]["value"], 1.0)
            self.assertEqual(report["metrics"]["attribution_accuracy"]["value"], 0.0)

    def test_attribution_requires_correct_text(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            scenario.data["expected"]["controller_behavior"] = {"normal": True, "keywords": ["正常"]}
            runner = FakeRunner(a2a_text="HPA 正常动作，aegis-eval 的 Pod 同时发生 OOM")
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT, mode="a2a")
            self.assertEqual(report["status"], "passed")
            self.assertEqual(report["metrics"]["attribution_accuracy"]["value"], 1.0)
            self.assertEqual(report["metrics"]["misrepair_rate"]["value"], 0.0)

    def test_existing_change_counts_for_mttr(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            committed = change("Committed", committed_time=datetime.now(timezone.utc).isoformat())
            runner = FakeRunner(before_changes=[change("Pending")], after_changes=[committed])
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            report, _ = run_scenarios([scenario], runner, Path(temporary), REPO_ROOT)
            result = report["scenarios"][0]
            self.assertIn("cr-new", result["new_change_names"])
            self.assertIsNotNone(result["mttr_seconds"])

    def test_multi_operation_risk_is_rejected(self) -> None:
        scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
        operation = change("Pending")
        operation["spec"]["operations"].append({"riskLevel": "R3"})
        result = grade_scenario(scenario.expected, {}, [operation], datetime.now(timezone.utc))
        self.assertFalse(result["disposition_hit"])

    def test_duplicate_agent_outputs_are_preserved(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            runner = FakeRunner()
            _, run_dir = run_scenarios([scenario, scenario], runner, Path(temporary), REPO_ROOT)
            self.assertTrue((run_dir / "00-oom-killed-agent-output.json").is_file())
            self.assertTrue((run_dir / "01-oom-killed-agent-output.json").is_file())

    def test_config_hash_tracks_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            scenario = load_scenario(SCENARIO_PATH, REPO_ROOT)
            other = Path(temporary) / "other"
            other.mkdir()
            (other / "manifest.yaml").write_text("different: true\n", encoding="utf-8")
            first = _scenario_config(scenario)["manifest_sha256"]
            second = _scenario_config(Scenario(path=other, data=scenario.data))["manifest_sha256"]
            self.assertNotEqual(first, second)


def change(state: str, committed_time: str | None = None) -> dict:
    conditions = []
    if committed_time:
        conditions = [{"type": "Committed", "lastTransitionTime": committed_time}]
    return {
        "metadata": {"name": "cr-new", "namespace": "kagent", "uid": "uid-new"},
        "spec": {"operations": [{"riskLevel": "R1"}]},
        "status": {"state": state, "conditions": conditions},
    }


if __name__ == "__main__":
    unittest.main()
