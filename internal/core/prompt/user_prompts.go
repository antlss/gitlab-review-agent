package prompt

import "fmt"

// ─── Reviewer User Message Components ───────────────────────────────────────────

const ReviewerAllDiffsHeader = "## All Changed Files — Diffs Pre-loaded\n"
const ReviewerAllDiffsFooter = "\nAll diffs are above. Do NOT call `get_multi_diff` for these files — use tools only to read context/dependency files.\n\n"
const ReviewerAllDiffsInstruction = "Begin your investigation. Analyze the pre-loaded diffs immediately."

const ReviewerHighRiskDiffsHeader = "## High-Risk Files — Diffs Pre-loaded\n"
const ReviewerHighRiskDiffsFooter = "\nHigh-risk diffs are above. Use `get_multi_diff` for any remaining files not shown.\n\n"
const ReviewerHighRiskDiffsInstruction = "Begin your investigation. Analyze the pre-loaded diffs first, then use tools for remaining files."

const ReviewerNoDiffsInstruction = "\nBegin your investigation. Start with HIGH RISK files using `get_multi_diff`."

// ─── Agent Budget Messages ──────────────────────────────────────────────────────

func BudgetWarning(iteration, max int) string {
	return fmt.Sprintf(
		"BUDGET WARNING: You have used %d/%d tool calls. "+
			"STOP exploring and emit '=== FINAL REVIEW ===' NOW with all findings collected so far. "+
			"Only make 1 more tool call if you have a HIGH-confidence lead on a critical/high severity bug.",
		iteration, max,
	)
}

func BudgetExhausted(max int) string {
	return fmt.Sprintf(
		"BUDGET EXHAUSTED. You have used all %d allowed tool calls. "+
			"Emit '=== FINAL REVIEW ===' immediately followed by your JSON output.",
		max,
	)
}

const AgentNudge = "Please either make a tool call to gather more information, or emit '=== FINAL REVIEW ===' followed by your JSON review output if you are ready."

// ─── External-Facing Localized Text ─────────────────────────────────────────────
// These strings appear in GitLab comments/replies and are language-dependent.

func SuggestionLabel(lang ResponseLanguage) string {
	switch lang {
	case LangVI:
		return "Gợi ý sửa:"
	case LangJA:
		return "修正案:"
	default:
		return "Suggested fix:"
	}
}

func SummaryLGTM() string {
	return "✅ **LGTM**.\n\n"
}

func SummaryAllFiltered(lang ResponseLanguage, count int) string {
	switch lang {
	case LangVI:
		return fmt.Sprintf("✅ **Không có comment nào được đăng.** AI phát hiện %d vấn đề tiềm năng nhưng tất cả đã bị lọc (ví dụ: độ tin cậy thấp, trùng lặp, không đủ ảnh hưởng production, hoặc không actionable).\n\n", count)
	case LangJA:
		return fmt.Sprintf("✅ **コメントは投稿されませんでした。** AIが%d件の潜在的な問題を検出しましたが、すべてフィルタリングされました（低信頼度、重複、本番影響不足、または非アクショナブルなど）。\n\n", count)
	default:
		return fmt.Sprintf("✅ **No comments posted.** AI found %d potential issues but all were filtered (for example low confidence, duplicate, non-production, or non-actionable).\n\n", count)
	}
}

func SummaryPostedCount(lang ResponseLanguage, count int) string {
	switch lang {
	case LangVI:
		return fmt.Sprintf("- **Vấn đề đã đăng:** %d\n", count)
	case LangJA:
		return fmt.Sprintf("- **投稿された問題:** %d\n", count)
	default:
		return fmt.Sprintf("- **Issues posted:** %d\n", count)
	}
}

func SummaryFilteredCount(lang ResponseLanguage, count int) string {
	switch lang {
	case LangVI:
		return fmt.Sprintf("- **Bị lọc:** %d (độ tin cậy thấp, trùng lặp, không đủ ảnh hưởng production, hoặc không actionable)\n", count)
	case LangJA:
		return fmt.Sprintf("- **フィルタリング:** %d（低信頼度、重複、本番影響不足、または非アクショナブル）\n", count)
	default:
		return fmt.Sprintf("- **Filtered:** %d (low confidence, duplicate, non-production, or non-actionable)\n", count)
	}
}

func SummaryAutoResolved(lang ResponseLanguage, count int) string {
	switch lang {
	case LangVI:
		return fmt.Sprintf("- **Auto-resolved:** %d (vấn đề trước đã được sửa trong commit mới)\n", count)
	case LangJA:
		return fmt.Sprintf("- **自動解決:** %d（以前のコミットで修正された問題）\n", count)
	default:
		return fmt.Sprintf("- **Auto-resolved:** %d (previously flagged issues fixed in new commit)\n", count)
	}
}

func SummaryReplyHint(lang ResponseLanguage) string {
	switch lang {
	case LangVI:
		return "\nReply vào bất kỳ comment nào để thảo luận. Bot sẽ phản hồi với đầy đủ context."
	case LangJA:
		return "\nコメントに返信して議論できます。ボットが完全なコンテキストで応答します。"
	default:
		return "\nReply to any comment to discuss. Bot will respond with full context."
	}
}
