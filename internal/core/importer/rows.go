// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Value preserves SQLite storage classes, NULL, exact integer precision and
// arbitrary text/blob bytes. Data encodes text and blobs as base64.
type Value struct {
	Kind string `json:"kind"`
	Data string `json:"data,omitempty"`
}
type Row map[string]Value

func value(v any) (Value, error) {
	switch v := v.(type) {
	case nil:
		return Value{Kind: "null"}, nil
	case int64:
		return Value{"integer", strconv.FormatInt(v, 10)}, nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return Value{}, ErrSnapshot
		}
		return Value{"real", strconv.FormatFloat(v, 'g', -1, 64)}, nil
	case string:
		return Value{"text", base64.StdEncoding.EncodeToString([]byte(v))}, nil
	case []byte:
		return Value{"blob", base64.StdEncoding.EncodeToString(v)}, nil
	default:
		return Value{}, ErrSnapshot
	}
}
func (r Row) text(k string) string {
	v := r[k]
	if v.Kind == "text" || v.Kind == "blob" {
		b, _ := base64.StdEncoding.DecodeString(v.Data)
		return string(b)
	}
	return v.Data
}
func (r Row) integer(k string) int64  { n, _ := strconv.ParseInt(r.text(k), 10, 64); return n }
func (r Row) number(k string) float64 { n, _ := strconv.ParseFloat(r.text(k), 64); return n }
func (r Row) clone() Row {
	c := Row{}
	for k, v := range r {
		c[k] = v
	}
	return c
}
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// RowHash hashes every typed column, with sorted object keys and no numeric
// coercion. It does not hash a SELECT rendering or the source database file.
func RowHash(r Row) string  { return digest(r) }
func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func readRows(ctx context.Context, db *sql.DB, table string, columns []Column) ([]Row, error) {
	var names []string
	for _, c := range columns {
		if c.Hidden == 0 {
			names = append(names, quote(c.Name))
		}
	}
	rows, err := db.QueryContext(ctx, "SELECT "+strings.Join(names, ",")+" FROM "+quote(table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := Row{}
		j := 0
		for _, c := range columns {
			if c.Hidden != 0 {
				continue
			}
			v, err := value(vals[j])
			if err != nil {
				return nil, err
			}
			r[c.Name] = v
			j++
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func rowKey(table string, r Row, columns []Column) string {
	var keys []Column
	for _, c := range columns {
		if c.PrimaryKey > 0 {
			keys = append(keys, c)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].PrimaryKey < keys[j].PrimaryKey })
	if len(keys) == 1 {
		return r.text(keys[0].Name)
	}
	if table == "host_uid_bindings" {
		return digest([]Value{r["lineage_key"], r["uid"], r["store_id"], r["kind"]})
	}
	var values []Value
	for _, c := range keys {
		values = append(values, r[c.Name])
	}
	if len(values) == 0 {
		return ""
	}
	return digest(values)
}
func checkRow(r Row) error {
	for name, v := range r {
		if name == "" {
			return ErrSnapshot
		}
		switch v.Kind {
		case "null":
			if v.Data != "" {
				return ErrSnapshot
			}
		case "text", "blob":
			if _, err := base64.StdEncoding.DecodeString(v.Data); err != nil {
				return ErrSnapshot
			}
		case "integer":
			n, err := strconv.ParseInt(v.Data, 10, 64)
			if err != nil || strconv.FormatInt(n, 10) != v.Data {
				return ErrSnapshot
			}
		case "real":
			n, err := strconv.ParseFloat(v.Data, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return ErrSnapshot
			}
		default:
			return fmt.Errorf("%w: storage class", ErrSnapshot)
		}
	}
	return nil
}
