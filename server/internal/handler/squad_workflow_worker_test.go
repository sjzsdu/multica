package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TestSiblingsAreStaged covers the helper used by autoFinalizeParent to decide
// whether staged siblings exist (at least one child has a non-null Stage).
func TestSiblingsAreStaged(t *testing.T) {
	tests := []struct {
		name     string
		children []db.Issue
		want     bool
	}{
		{"empty slice", []db.Issue{}, false},
		{"all children have nil stage", []db.Issue{
			{Stage: pgtype.Int4{Valid: false}},
			{Stage: pgtype.Int4{Valid: false}},
		}, false},
		{"first child has staged", []db.Issue{
			{Stage: pgtype.Int4{Int32: 0, Valid: true}},
			{Stage: pgtype.Int4{Valid: false}},
		}, true},
		{"second child has staged", []db.Issue{
			{Stage: pgtype.Int4{Valid: false}},
			{Stage: pgtype.Int4{Int32: 1, Valid: true}},
		}, true},
		{"all children have staged", []db.Issue{
			{Stage: pgtype.Int4{Int32: 0, Valid: true}},
			{Stage: pgtype.Int4{Int32: 0, Valid: true}},
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := siblingsAreStaged(tt.children)
			if got != tt.want {
				t.Errorf("siblingsAreStaged() = %v, want %v", got, tt.want)
			}
		})
	}
}
