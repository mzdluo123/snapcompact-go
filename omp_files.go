package snapcompact

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// OMPFileOperations records which paths the archived turns read and changed.
type OMPFileOperations struct {
	Read, Written, Edited []string
}

var ompURLPath = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://`)

func (operations OMPFileOperations) Lists() (read, modified []string) {
	seen := make(map[string]bool)
	for _, files := range [][]string{operations.Edited, operations.Written} {
		for _, name := range files {
			if !seen[name] && !ompURLPath.MatchString(name) {
				seen[name] = true
				modified = append(modified, name)
			}
		}
	}
	for _, name := range operations.Read {
		if !seen[name] && !ompURLPath.MatchString(name) {
			seen[name] = true
			read = append(read, name)
		}
	}
	slices.Sort(read)
	slices.Sort(modified)
	return read, modified
}

type ompTree struct {
	name, mark  string
	files, dirs []*ompTree
}

func ompFileList(read, modified, allRead []string) string {
	modes := make(map[string]string)
	paths := append(append([]string{}, read...), modified...)
	slices.Sort(paths)
	for _, name := range read {
		modes[name] = "Read"
	}
	readSet := make(map[string]bool)
	for _, name := range allRead {
		readSet[name] = true
	}
	for _, name := range modified {
		modes[name] = "Write"
		if readSet[name] {
			modes[name] = "RW"
		}
	}
	root := &ompTree{}
	for _, name := range paths[:min(20, len(paths))] {
		s := strings.ReplaceAll(name, "\\", "/")
		dir := strings.HasSuffix(s, "/")
		s = strings.TrimSuffix(s, "/")
		if s == "" {
			continue
		}
		parts := strings.Split(s, "/")
		dirs := len(parts)
		if !dir {
			dirs--
		}
		node := root
		for _, part := range parts[:dirs] {
			j := slices.IndexFunc(node.dirs, func(child *ompTree) bool { return child.name == part })
			if j < 0 {
				j = len(node.dirs)
				node.dirs = append(node.dirs, &ompTree{name: part})
			}
			node = node.dirs[j]
		}
		if !dir && !slices.ContainsFunc(node.files, func(file *ompTree) bool { return file.name == parts[dirs] }) {
			node.files = append(node.files, &ompTree{name: parts[dirs], mark: modes[name]})
		}
	}
	var lines []string
	var walk func(*ompTree, int)
	walk = func(node *ompTree, depth int) {
		for _, file := range node.files {
			lines = append(lines, file.name+" ("+file.mark+")")
		}
		for _, child := range node.dirs {
			parts := []string{child.name}
			for len(child.files) == 0 && len(child.dirs) == 1 {
				child = child.dirs[0]
				parts = append(parts, child.name)
			}
			lines = append(lines, strings.Repeat("#", depth+1)+" "+strings.Join(parts, "/")+"/")
			walk(child, depth+1)
		}
	}
	walk(root, 0)
	if len(paths) > 20 {
		lines = append(lines, fmt.Sprintf("[…%d files elided…]", len(paths)-20))
	}
	return strings.Join(lines, "\n")
}
