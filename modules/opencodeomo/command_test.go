package opencodeomo

import (
	"strings"
	"testing"
)

func TestOmoSlotCardKeepsIdentifiersWhole(t *testing.T) {
	values := []string{
		"omo-" + strings.Repeat("slot", 12),
		"agent/" + strings.Repeat("worker", 8),
		"provider/" + strings.Repeat("original-model", 4),
		"normal",
		"heavy",
		"provider/" + strings.Repeat("effective-model", 4),
	}
	got := omoSlotCard(values[0], values[1], values[2], values[3], values[4], values[5])
	for _, value := range values {
		if !strings.Contains(got, value) {
			t.Fatalf("omoSlotCard split %q:\n%s", value, got)
		}
	}
}
