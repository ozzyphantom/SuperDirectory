package engine

import (
	"fmt"
	"path/filepath"

	"github.com/ozzyphantom/SuperDirectory/internal/flatten"
)

// batch splits the plan into folders of at most size files each — "Batch 01",
// "Batch 02" — in plan order, keeping each file's layout inside its batch. A
// NotebookLM notebook holds 50 sources on the free plan; one batch is one
// notebook's worth. It returns the number of batches.
func batch(items []flatten.Item, size int) int {
	if size <= 0 || len(items) == 0 {
		return 0
	}
	n := (len(items) + size - 1) / size
	width := len(fmt.Sprint(n))
	if width < 2 {
		width = 2
	}
	for i := range items {
		folder := fmt.Sprintf("Batch %0*d", width, i/size+1)
		items[i].Want = filepath.Join(folder, items[i].Want)
	}
	flatten.Assign(items)
	return n
}
