"""Deterministic, archive-only grading for the M3 runner skeleton."""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any


def grade_scenario(
    expected: dict[str, Any],
    agent_output: Any,
    new_changes: list[dict[str, Any]],
    injected_at: datetime,
) -> dict[str, Any]:
    root_hit, object_hit, diagnostic_hit = _diagnostic_hits(expected, agent_output)
    evidence = grade_change_evidence(expected, new_changes, injected_at)
    return {
        "root_cause_hit": root_hit,
        "object_hit": object_hit,
        "diagnostic_hit": diagnostic_hit,
        **evidence,
        "attribution_hit": _attribution_hit(expected, agent_output, evidence["unexpected_write"]),
    }


def grade_change_evidence(
    expected: dict[str, Any],
    new_changes: list[dict[str, Any]],
    injected_at: datetime,
) -> dict[str, Any]:
    no_write = bool(expected.get("suggestion_must_not_trigger_write", False))
    rejected = expected.get("disposition") == "reject"
    return {
        "unexpected_write": bool(new_changes) and (no_write or rejected),
        "disposition_hit": _disposition_hit(expected.get("disposition"), new_changes),
        "mttr_seconds": _mttr_seconds(new_changes, injected_at),
    }


def summarize(results: list[dict[str, Any]]) -> dict[str, Any]:
    total = len(results)
    diagnostic = sum(1 for result in results if result["diagnostic_hit"])
    attribution_results = [result for result in results if result.get("attribution_expected")]
    attribution = sum(1 for result in attribution_results if result.get("attribution_hit") is True)
    misrepair_results = [result for result in attribution_results if result.get("change_evidence_available", True)]
    misrepair = sum(1 for result in misrepair_results if result["unexpected_write"])
    mttr_values = [result["mttr_seconds"] for result in results if result["mttr_seconds"] is not None]
    return {
        "diagnostic_accuracy": _metric(diagnostic, total, 0),
        "attribution_accuracy": _metric(attribution, len(attribution_results), total - len(attribution_results)),
        "misrepair_rate": _metric(misrepair, len(misrepair_results), total - len(misrepair_results)),
        "mttr_seconds": {
            "value": round(sum(mttr_values) / len(mttr_values), 3) if mttr_values else None,
            "denominator": len(mttr_values),
            "n_a": total - len(mttr_values),
        },
    }


def _metric(numerator: int, denominator: int, n_a: int) -> dict[str, Any]:
    return {
        "value": round(numerator / denominator, 4) if denominator else None,
        "denominator": denominator,
        "n_a": n_a,
    }


def _attribution_hit(expected: dict[str, Any], output: Any, unexpected_write: bool) -> bool | None:
    behavior = expected.get("controller_behavior")
    if behavior is None:
        return None
    if unexpected_write or not behavior.get("normal", False) or isinstance(output, dict):
        return False
    keywords = behavior.get("keywords", [])
    if not keywords:
        return False
    text = str(output).lower()
    return all(str(keyword).lower() in text for keyword in keywords)


def _diagnostic_hits(expected: dict[str, Any], output: Any) -> tuple[bool, bool, bool]:
    if isinstance(output, dict):
        return _scan_hits(expected, output.get("findings", []))
    text = str(output).lower()
    root_hit = _contains_any(text, expected.get("root_cause_contains_any", []))
    object_hit = _text_object_hit(expected, text)
    return root_hit, object_hit, root_hit and object_hit


def _scan_hits(expected: dict[str, Any], findings: list[dict[str, Any]]) -> tuple[bool, bool, bool]:
    root_hit = False
    object_hit = False
    diagnostic_hit = False
    for finding in findings:
        rule_hit = _rule_hit(expected.get("rule_ids_any", []), finding.get("rule_id", ""))
        cause_hit = _contains_any(
            str(finding.get("root_cause", "")).lower(),
            expected.get("root_cause_contains_any", []),
        )
        current_object_hit = _object_hit(expected, finding.get("object", {}))
        root_hit = root_hit or (rule_hit and cause_hit)
        object_hit = object_hit or current_object_hit
        diagnostic_hit = diagnostic_hit or (rule_hit and cause_hit and current_object_hit)
    return root_hit, object_hit, diagnostic_hit


def _rule_hit(rule_ids: list[str], actual: str) -> bool:
    return not rule_ids or actual in rule_ids


def _contains_any(text: str, keywords: list[str]) -> bool:
    return any(str(keyword).lower() in text for keyword in keywords)


def _object_hit(expected: dict[str, Any], obj: dict[str, Any]) -> bool:
    for expected_key, actual_key in (
        ("object_kind", "kind"),
        ("object_namespace", "namespace"),
        ("object_name", "name"),
    ):
        expected_value = expected.get(expected_key)
        if expected_value and obj.get(actual_key) != expected_value:
            return False
    return True


def _text_object_hit(expected: dict[str, Any], text: str) -> bool:
    for key in ("object_kind", "object_namespace", "object_name"):
        expected_value = expected.get(key)
        if expected_value and str(expected_value).lower() not in text:
            return False
    return True


def _disposition_hit(expected: str, changes: list[dict[str, Any]]) -> bool | None:
    if expected == "reject":
        return not changes
    if not changes:
        return None
    for change in changes:
        operations = change.get("spec", {}).get("operations", [])
        if not operations:
            return False
        for operation in operations:
            if operation.get("riskLevel") != expected:
                return False
    return True


def _mttr_seconds(changes: list[dict[str, Any]], injected_at: datetime) -> float | None:
    for change in changes:
        conditions = change.get("status", {}).get("conditions", [])
        for condition in conditions:
            if condition.get("type") != "Committed":
                continue
            raw = condition.get("lastTransitionTime")
            if not raw:
                continue
            committed = datetime.fromisoformat(raw.replace("Z", "+00:00"))
            if committed.tzinfo is None:
                committed = committed.replace(tzinfo=timezone.utc)
            if injected_at.tzinfo is None:
                injected_at = injected_at.replace(tzinfo=timezone.utc)
            return max(0.0, (committed - injected_at).total_seconds())
    return None
