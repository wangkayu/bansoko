package domain

import "testing"

func TestParserSelectionSpanIsOptionalAndBounded(t *testing.T) {
	input := fullInput(t)
	withSpan := func(span int64) Value {
		value := rebuild(input, []string{"config", "parser", "selection", "value"}, func(v Value) Value {
			v.Object["span"] = i64(span)
			return v
		})
		return rebuild(value, []string{"config", "parser", "provenance", "value", "selection"}, func(v Value) Value {
			v.Object["span"] = i64(span)
			return v
		})
	}
	for _, span := range []int64{1, 2, 12} {
		if err := Validate("AcquisitionInputV1", withSpan(span)); err != nil {
			t.Fatalf("span %d: %v", span, err)
		}
	}
	for _, span := range []int64{0, 13} {
		if err := Validate("AcquisitionInputV1", withSpan(span)); err == nil {
			t.Fatalf("out-of-range span %d accepted", span)
		}
	}
}
