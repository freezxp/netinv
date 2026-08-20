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

// Str converts an SNMP text value.
//
// OctetStrings arrive as []byte, not string — gosnmp returns them that way and
// so does the fixture loader. A plain `v.(string)` assertion therefore fails on
// every text column on real hardware while looking perfectly correct, which is
// how a Cisco CPU series ended up labelled by table index instead of by the
// processor's name. Num has a matching problem and solves it the same way.
func Str(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case []byte:
		return string(x), true
	}
	return "", false
}

// GaugeSample builds a health sample with bounded labels.
func GaugeSample(name string, labels map[string]string, value float64) Sample {
	return Sample{Name: name, Labels: labels, Value: value, At: time.Now().UTC()}
}
