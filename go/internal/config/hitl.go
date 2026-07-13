package config

import (
	"os"
	"strings"
)

// HITLConfig ports config.py HITLConfig — the human-in-the-loop review gate.
// Enabled and ApprovalUserID are env-driven (read at call time); the two numeric
// caps are plain defaults (§D: approval_expires_in_hours=72,
// max_review_revisions=2). HITL is actually active only when HAX_API_KEY is set
// — the same trigger the hax client uses; this struct mirrors it for
// observability.
type HITLConfig struct {
	Enabled bool `json:"enabled"`
	// ApprovalUserID optionally routes the request to a specific hax user (nil
	// when unset/blank).
	ApprovalUserID         *string `json:"approval_user_id"`
	ApprovalExpiresInHours int     `json:"approval_expires_in_hours"`
	MaxReviewRevisions     int     `json:"max_review_revisions"`
}

// DefaultHITLConfig builds the config.py HITL defaults, reading HAX_API_KEY and
// AGENTFIELD_APPROVAL_USER_ID at call time.
func DefaultHITLConfig() HITLConfig {
	return HITLConfig{
		Enabled:                strings.TrimSpace(os.Getenv("HAX_API_KEY")) != "",
		ApprovalUserID:         approvalUserID(),
		ApprovalExpiresInHours: 72,
		MaxReviewRevisions:     2,
	}
}

// approvalUserID ports `os.getenv("AGENTFIELD_APPROVAL_USER_ID") or None`: an
// unset OR empty value yields nil.
func approvalUserID() *string {
	v := os.Getenv("AGENTFIELD_APPROVAL_USER_ID")
	if v == "" {
		return nil
	}
	return &v
}
