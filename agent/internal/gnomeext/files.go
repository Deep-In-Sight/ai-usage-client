package gnomeext

import (
	"fmt"
	"io/fs"
	"path"
)

// formFiles is the embedded form's files by their slash path in the
// extension's folder ("extension.js").
func formFiles(form string) (map[string][]byte, error) {
	root := path.Join(form, UUID)
	files := map[string][]byte{}
	err := fs.WalkDir(forms, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := forms.ReadFile(p)
		if err != nil {
			return err
		}
		files[p[len(root)+1:]] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("the embedded extension form %q: %w", form, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no embedded extension form %q", form)
	}
	return files, nil
}
