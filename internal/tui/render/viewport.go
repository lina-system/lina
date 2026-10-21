// SPDX-License-Identifier: Apache-2.0

package render

type Viewport struct{ Top, Rows, Overscan int }
type Change struct {
	Row  int
	Text string
}
type Painter func(Row) string

type cacheKey struct {
	Row    Row
	Config Config
}

// Renderer retains only the current viewport plus overscan. Draw returns row
// replacements; no replacements means zero output. A UI may run Draw on its
// render worker and discard stale results using the reducer generation.
type Renderer struct {
	cache    map[cacheKey]string
	previous []string
}

// Draw touches at most viewport rows plus overscan on either side, independent
// of the source history length. The source supplies its own row index. Paint
// runs only on cache misses and receives sanitized, clipped text for styling.
func (r *Renderer) Draw(source Source, view Viewport, config Config, paint Painter) []Change {
	count := max(0, source.Len())
	top := min(count, max(0, view.Top))
	height := min(count-top, max(0, view.Rows))
	over := max(0, view.Overscan)
	start := top - min(top, over)
	end := top + height + min(count-top-height, over)
	if height == 0 {
		start = top
		end = top
	}
	current := make([]string, height)
	cache := make(map[cacheKey]string, end-start)
	m := Unicode{WidthMode: config.Mode}
	for i := start; i < end; i++ {
		row := source.At(i)
		key := cacheKey{row, config}
		painted, ok := r.cache[key]
		if !ok {
			row.Text = Truncate(row.Text, config.Width, m)
			painted = row.Text
			if paint != nil && row.Done {
				painted = paint(row)
			}
		}
		cache[key] = painted
		if i >= top && i < top+height {
			current[i-top] = painted
		}
	}
	var changes []Change
	for i := 0; i < max(len(current), len(r.previous)); i++ {
		text := ""
		if i < len(current) {
			text = current[i]
		}
		if i >= len(current) || i >= len(r.previous) || text != r.previous[i] {
			changes = append(changes, Change{i, text})
		}
	}
	r.previous = current
	r.cache = cache
	return changes
}
