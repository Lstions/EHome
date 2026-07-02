package api

import (
	"testing"

	"ehome/backend/internal/models"
)

func TestDownsampleUnifiedData(t *testing.T) {
	tests := []struct {
		name      string
		data      []models.UnifiedData
		maxPoints int
		wantLen   int
	}{
		{
			name:      "nil data",
			data:      nil,
			maxPoints: 100,
			wantLen:   0,
		},
		{
			name:      "empty data",
			data:      []models.UnifiedData{},
			maxPoints: 100,
			wantLen:   0,
		},
		{
			name:      "maxPoints <= 0 returns unchanged",
			data:      make([]models.UnifiedData, 100),
			maxPoints: 0,
			wantLen:   100,
		},
		{
			name:      "data smaller than maxPoints returns unchanged",
			data:      make([]models.UnifiedData, 50),
			maxPoints: 100,
			wantLen:   50,
		},
		{
			name:      "data equals maxPoints returns unchanged",
			data:      make([]models.UnifiedData, 100),
			maxPoints: 100,
			wantLen:   100,
		},
		{
			name:      "1000 points to 500",
			data:      make([]models.UnifiedData, 1000),
			maxPoints: 500,
			wantLen:   501, // 500 + 1 (last point)
		},
		{
			name:      "10000 points to 100",
			data:      make([]models.UnifiedData, 10000),
			maxPoints: 100,
			wantLen:   101, // 100 + 1 (last point)
		},
		{
			name:      "2 points to 500 (no change)",
			data:      make([]models.UnifiedData, 2),
			maxPoints: 500,
			wantLen:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := downsampleUnifiedData(tt.data, tt.maxPoints)
			if len(result) != tt.wantLen {
				t.Errorf("downsampleUnifiedData() returned %d items, want %d", len(result), tt.wantLen)
			}
		})
	}
}

func TestDownsampleUnifiedDataPreservesEndpoints(t *testing.T) {
	// Create 1000 points with known values
	data := make([]models.UnifiedData, 1000)
	for i := range data {
		data[i] = models.UnifiedData{
			ID:    uint(i + 1),
			Value: float64(i),
		}
	}

	result := downsampleUnifiedData(data, 100)

	if len(result) < 2 {
		t.Fatal("expected at least 2 points")
	}

	// First point must be preserved
	if result[0].ID != data[0].ID {
		t.Errorf("first point ID = %d, want %d", result[0].ID, data[0].ID)
	}

	// Last point must be preserved
	if result[len(result)-1].ID != data[len(data)-1].ID {
		t.Errorf("last point ID = %d, want %d", result[len(result)-1].ID, data[len(data)-1].ID)
	}
}
