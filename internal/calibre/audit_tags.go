package calibre

import (
	"context"
	"fmt"
	"strings"

	"github.com/vavallee/bindery/internal/db"
)

const auditTagSetting = "calibre.audit_tag_write_enabled"

// ReconcileAuditTags retries the optional tag projection after a reviewer
// ignores a finding. It serializes with full audit passes; the desired state is
// always read from committed Bindery findings, never from a caller's snapshot.
func (s *AuthoritativeService) ReconcileAuditTags(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.passMu.Lock()
	defer s.passMu.Unlock()
	_, err := s.reconcileAuditTags(ctx)
	return err
}

func (s *AuthoritativeService) reconcileAuditTags(ctx context.Context) (int, error) {
	if !s.IsEnabled(ctx) || s.audits == nil {
		return 0, nil
	}
	enabled, err := s.settings.Get(ctx, auditTagSetting)
	if err != nil {
		return 0, fmt.Errorf("read calibre audit tag opt-in: %w", err)
	}
	if enabled == nil || !strings.EqualFold(enabled.Value, "true") {
		return 0, nil
	}
	desired, err := s.audits.ActionableCalibreIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("read actionable calibre audit findings: %w", err)
	}
	// The authoritative reader never obtains a writable handle. Only this
	// purpose-built writer opens metadata.db for mutation; it cannot update
	// non-tag fields or select a different tag at the call site.
	return db.NewCalibreTagWriter(s.LibraryPath(ctx)).ReconcileMismatch(ctx, desired)
}
