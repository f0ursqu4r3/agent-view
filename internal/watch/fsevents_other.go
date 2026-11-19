//go:build !darwin

package watch

// native is the backend of Tree outside macOS: FSEvents exists only there,
// so Tree uses fsnotify (inotify on Linux).
func native(root string, onChange func(rel string, op Op), o Options) Watcher {
	return treeFsnotify(root, onChange, o)
}
