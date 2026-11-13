package watchmaker

import (
	"strconv"
	"testing"
	"time"
)

func TestCalculateOffsetAbsoluteNanoseconds(t *testing.T) {
	for _, year := range []int{2003, 2040} {
		target := time.Date(year, 1, 1, 0, 0, 0, 999999999, time.UTC)
		for _, input := range []string{target.Format("2006-01-02 15:04:05.999999999"), strconv.FormatInt(target.UnixNano(), 10)} {
			t.Run(input, func(t *testing.T) {
				before := time.Now()
				offset, err := CalculateOffset(input)
				after := time.Now()
				if err != nil {
					t.Fatal(err)
				}
				if offset < target.Sub(after) || offset > target.Sub(before) {
					t.Fatalf("offset %s does not point to %s; expected [%s, %s]", offset, target, target.Sub(after), target.Sub(before))
				}
			})
		}
	}
}
