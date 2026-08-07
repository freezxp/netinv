package sdk

import (
	"context"
	"strings"
	"time"
)

// WalkColumn walks a table column and yields (rowIndex, value) pairs —
// the shared idiom of every vendor health map.
func WalkColumn(ctx context.Context, s Session, columnOID string,
	fn func(index string, v Var)) error {
	vars, err := s.Walk(ctx, columnOID)
	if err != nil {
		return err
	}
	for _, v := range vars {
		if idx, ok := strings.CutPrefix(v.OID, columnOID+"."); ok {
			fn(idx, v)
		}
	}
	return nil
}

// Num converts an SNMP numeric value.
func Num(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// GaugeSample builds a health sample with bounded labels.
func GaugeSample(name string, labels map[string]string, value float64) Sample {
	return Sample{Name: name, Labels: labels, Value: value, At: time.Now().UTC()}
}
