package reply

import (
	"github.com/antlss/gitlab-review-agent/internal/domain"
	"strings"
)

var intentKeywords = map[domain.ReplyIntent][]string{
	domain.IntentReject: {
		"false positive", "intentional", "not an issue", "by design", "disagree",
		"won't fix", "incorrect", "wrong", "sai",
	},
	domain.IntentQuestion: {
		"why", "how", "what", "explain", "?",
	},
	domain.IntentDiscuss: {
		"what about", "how about", "alternatively", "instead",
	},
	domain.IntentAgree: {
		"fixed", "done", "agree", "good catch", "will fix", "ok", "thanks", "thank you",
	},
	domain.IntentAcknowledge: {
		"noted", "ack", "will address later",
	},
}

// ClassifyIntent determines the intent of a user's reply based on keyword matching.
func ClassifyIntent(text string) domain.ReplyIntent {
	lower := strings.ToLower(text)

	// Priority order
	for _, intent := range []domain.ReplyIntent{
		domain.IntentReject, domain.IntentQuestion, domain.IntentDiscuss,
		domain.IntentAgree, domain.IntentAcknowledge,
	} {
		for _, kw := range intentKeywords[intent] {
			if strings.Contains(lower, kw) {
				return intent
			}
		}
	}
	return domain.IntentAcknowledge
}

// IntentToSignal maps a reply intent to a feedback signal.
func IntentToSignal(intent domain.ReplyIntent) domain.FeedbackSignal {
	switch intent {
	case domain.IntentAgree, domain.IntentAcknowledge:
		return domain.FeedbackSignalAccepted
	case domain.IntentReject:
		return domain.FeedbackSignalRejected
	default:
		return domain.FeedbackSignalNeutral
	}
}
