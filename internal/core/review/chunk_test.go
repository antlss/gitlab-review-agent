package review

import (
	"fmt"
	"testing"

	"github.com/antlss/gitlab-review-agent/internal/domain"
)

func TestChunkFiles_SmallMR(t *testing.T) {
	files := makeDiffFiles("a.go", "b.go", "c.go")
	chunks := ChunkFiles(files, 10)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if len(chunks[0]) != 3 {
		t.Fatalf("expected 3 files in chunk, got %d", len(chunks[0]))
	}
}

func TestChunkFiles_GroupsByDomain(t *testing.T) {
	files := makeDiffFiles(
		"domains/partner/biz/invite.go",
		"domains/partner/biz/validate.go",
		"domains/bonus/biz/publish.go",
		"domains/bonus/biz/consume.go",
		"entities/enum/job.go",
		"entities/enum/s3.go",
		"singleton/csv/marshal.go",
	)
	chunks := ChunkFiles(files, 3)

	// Should produce at least 2 chunks since we have 7 files with target 3
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// Verify domain grouping: partner files should be together
	for _, chunk := range chunks {
		hasPartner := false
		hasBonus := false
		for _, f := range chunk {
			if f.Path == "domains/partner/biz/invite.go" || f.Path == "domains/partner/biz/validate.go" {
				hasPartner = true
			}
			if f.Path == "domains/bonus/biz/publish.go" || f.Path == "domains/bonus/biz/consume.go" {
				hasBonus = true
			}
		}
		if hasPartner && hasBonus {
			t.Error("partner and bonus files should be in separate chunks")
		}
	}
}

func TestChunkFiles_Empty(t *testing.T) {
	chunks := ChunkFiles(nil, 10)
	if chunks != nil {
		t.Fatalf("expected nil, got %v", chunks)
	}
}

func TestChunkFiles_LargeGroup(t *testing.T) {
	// 15 files in same domain with target 5 → should split into 3 chunks
	var files []domain.DiffFile
	for i := 0; i < 15; i++ {
		files = append(files, domain.DiffFile{
			Path:      fmt.Sprintf("domains/big/biz/file_%d.go", i),
			RiskScore: 10,
		})
	}
	chunks := ChunkFiles(files, 5)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}
}

func TestDomainKey(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"domains/partner/biz/invite.go", "domains/partner"},
		{"domains/bonus/biz/publish.go", "domains/bonus"},
		{"entities/enum/job.go", "entities/enum"},
		{"singleton/csv/marshal.go", "singleton/csv"},
		{"go.mod", "_root"},
		{"gen/db/entities.go", "gen/db"},
	}
	for _, tt := range tests {
		got := domainKey(tt.path)
		if got != tt.want {
			t.Errorf("domainKey(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestCalculateBudgetWithPreload(t *testing.T) {
	// With preload, budgets should be tighter
	maxNoPreload, _ := CalculateBudget(10)
	maxPreloaded, _ := CalculateBudgetWithPreload(10, true)

	if maxPreloaded >= maxNoPreload {
		t.Errorf("preloaded budget (%d) should be less than non-preloaded (%d)", maxPreloaded, maxNoPreload)
	}

	// Verify specific values for chunked sizes
	max, warn := CalculateBudgetWithPreload(3, true)
	if max != 5 || warn != 3 {
		t.Errorf("expected max=5 warn=3 for 3 files preloaded, got max=%d warn=%d", max, warn)
	}

	max, warn = CalculateBudgetWithPreload(5, true)
	if max != 8 || warn != 5 {
		t.Errorf("expected max=8 warn=5 for 5 files preloaded, got max=%d warn=%d", max, warn)
	}

	max, warn = CalculateBudgetWithPreload(10, true)
	if max != 10 || warn != 7 {
		t.Errorf("expected max=10 warn=7 for 10 files preloaded, got max=%d warn=%d", max, warn)
	}
}

func makeDiffFiles(paths ...string) []domain.DiffFile {
	files := make([]domain.DiffFile, len(paths))
	for i, p := range paths {
		files[i] = domain.DiffFile{
			Path:      p,
			RiskScore: float64(len(paths) - i), // descending risk
		}
	}
	return files
}
