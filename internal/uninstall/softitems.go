package uninstall

// Purpose: the plugin and vault-entry items. A plugin goes through
//   PluginLister.Remove with the teardown hook, so its own uninstall hook
//   removes its artifacts; a vault entry is deleted only while the custody
//   index still lists it.
// Inputs: Deps.Plugins, Deps.Teardown, Deps.Custody.
// Outputs: pluginItems, vaultItems.
// Constraints: only names the custody's List returns become items, so an
//   entry under another service (which this custody does not index) is
//   never named. Remove re-lists before Delete. A dry run lists only; the
//   delete custody is selected on the first real Delete.
//   Residual: a plugin's teardown hook is the plugin's own code; Cascade
//   bounds its deadline, not which files it deletes.
// SPORT: internal/uninstall plugin and vault items/ADDED.

import (
	"context"
	"slices"
	"sync"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// pluginItem is one installed plugin.
type pluginItem struct {
	name string
	d    Deps
}

func (p pluginItem) Kind() Kind      { return KindPlugin }
func (p pluginItem) Locator() string { return "plugin:" + p.name }
func (p pluginItem) Owner() string   { return p.name }
func (p pluginItem) Class() Class    { return ClassInstall }

// Remove runs the plugin's teardown and deletes its record.
func (p pluginItem) Remove(ctx context.Context, dryRun bool) (Delta, error) {
	delta := Delta{Kind: KindPlugin, Locator: p.Locator(), Action: ActionWouldRemove}
	if dryRun {
		return delta, nil
	}
	err := p.d.Plugins.Remove(ctx, p.d.Teardown, p.name)
	switch {
	case err == nil:
		delta.Action = ActionRemoved
	case isNotFound(err):
		delta.Action = ActionAbsent
	default:
		delta.Action = ActionKept
		return delta, err
	}
	return delta, nil
}

// pluginItems lists the installed plugins.
func pluginItems(ctx context.Context, d Deps) ([]Item, error) {
	if d.Plugins == nil {
		return nil, nil
	}
	names, err := d.Plugins.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(names))
	for _, n := range names {
		out = append(out, pluginItem{name: n, d: d})
	}
	return out, nil
}

// vaultItem is one indexed vault entry.
type vaultItem struct {
	name    string
	custody secrets.Custody
	writer  *vaultWriter
}

// vaultWriter selects the delete custody once, on first use.
type vaultWriter struct {
	once   sync.Once
	sel    func() (secrets.Custody, error)
	listed secrets.Custody
	c      secrets.Custody
	err    error
}

// get returns the delete custody, refusing a different backend.
func (w *vaultWriter) get() (secrets.Custody, error) {
	w.once.Do(func() {
		if w.sel == nil {
			w.c = w.listed
			return
		}
		w.c, w.err = w.sel()
		if w.err != nil {
			w.err = cascade.Wrap(cascade.KindUnavailable, w.err, "uninstall: select the vault custody for deletes")
		}
		if w.err == nil && w.c == nil {
			w.err = cascade.New(cascade.KindUnavailable, "uninstall: no vault custody was selected for deletes")
		}
		if w.err == nil && w.c.Name() != w.listed.Name() {
			w.err = cascade.Newf(cascade.KindUnavailable,
				"uninstall: the vault lists entries in %s but would delete through %s; refusing", w.listed.Name(), w.c.Name())
		}
	})
	return w.c, w.err
}

func (v vaultItem) Kind() Kind      { return KindVaultEntry }
func (v vaultItem) Locator() string { return "vault:" + v.name }
func (v vaultItem) Owner() string   { return v.custody.Name() }
func (v vaultItem) Class() Class    { return ClassInstall }

// Remove deletes the entry if the index still lists it.
func (v vaultItem) Remove(ctx context.Context, dryRun bool) (Delta, error) {
	delta := Delta{Kind: KindVaultEntry, Locator: v.Locator(), Action: ActionAbsent}
	names, err := v.custody.List(ctx)
	if err != nil {
		delta.Action = ActionKept
		return delta, err
	}
	if !slices.Contains(names, v.name) {
		return delta, nil
	}
	if dryRun {
		delta.Action = ActionWouldRemove
		return delta, nil
	}
	writer := v.writer
	if writer == nil {
		writer = &vaultWriter{listed: v.custody}
	}
	w, err := writer.get()
	if err == nil {
		err = w.Delete(ctx, v.name)
	}
	switch {
	case err == nil:
		delta.Action = ActionRemoved
	case isNotFound(err):
	default:
		delta.Action = ActionKept
		return delta, err
	}
	return delta, nil
}

// vaultItems is one item per name the custody indexes.
func vaultItems(ctx context.Context, d Deps) ([]Item, error) {
	if d.Custody == nil {
		return nil, nil
	}
	c, err := d.Custody()
	if err != nil {
		return nil, err
	}
	names, err := c.List(ctx)
	if err != nil {
		return nil, err
	}
	writer := &vaultWriter{sel: d.CustodyForDelete, listed: c}
	out := make([]Item, 0, len(names))
	for _, n := range names {
		out = append(out, vaultItem{name: n, custody: c, writer: writer})
	}
	return out, nil
}

// isNotFound reports a typed not-found error.
func isNotFound(err error) bool {
	k, ok := cascade.KindOf(err)
	return ok && k == cascade.KindNotFound
}
