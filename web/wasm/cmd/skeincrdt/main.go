//go:build js && wasm

// Command skeincrdt is the WebAssembly module the browser loads (ADR-0007).
// It registers one global object, skeinCrdt, whose functions wrap the bridge
// package one to one. Documents are addressed by integer handles because a
// Go pointer cannot cross into JavaScript.
//
// Conventions at this boundary, which web/src/crdt/wasm.ts relies on:
//   - 64-bit integers (replica IDs, counters) travel as decimal strings;
//   - operation and attribute batches travel as Uint8Array;
//   - positions are code points (the TypeScript side converts from UTF-16);
//   - a failure returns {error: "..."} instead of throwing, because a Go
//     panic would end the module for good.
package main

import (
	"strconv"
	"syscall/js"

	"github.com/genuinebnt/skein-go/libs/blocks"
	"github.com/genuinebnt/skein-go/web/wasm/bridge"
)

var (
	documents  = map[int]*bridge.Document{}
	nextHandle = 1
)

func main() {
	api := map[string]any{
		"newDocument":           fn(newDocument),
		"free":                  fn(free),
		"applyRemote":           fn(applyRemote),
		"applyRemoteAttrs":      fn(applyRemoteAttrs),
		"insert":                fn(insert),
		"delete":                fn(deleteText),
		"setAttrs":              fn(setAttrs),
		"seedAttrsFromMarkdown": fn(seedAttrs),
		"text":                  fn(text),
		"blocks":                fn(blockList),
		"version":               fn(version),
		"lamport":               fn(lamport),
		"opsSince":              fn(opsSince),
		"allAttrs":              fn(allAttrs),
		"idAt":                  fn(idAt),
		"indexOf":               fn(indexOf),
		"markdown":              fn(markdown),
		"markdownAt":            fn(markdownAt),
		"exportMapped":          fn(exportMapped),
		"renderInline":          fn(renderInline),
		"describeOps":           fn(describeOps),
		"checkInvariants":       fn(checkInvariants),
		"replaceWithMarkdown":   fn(replaceWithMarkdown),
	}
	js.Global().Set("skeinCrdt", js.ValueOf(api))
	js.Global().Call("dispatchEvent", js.Global().Get("Event").New("skeincrdtready"))
	select {} // keep the exported functions alive
}

func fn(f func(args []js.Value) any) js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) any { return f(args) })
}

func failure(err error) any { return map[string]any{"error": err.Error()} }

func doc(args []js.Value) (*bridge.Document, any) {
	d, ok := documents[args[0].Int()]
	if !ok {
		return nil, map[string]any{"error": "skeincrdt: unknown document handle"}
	}
	return d, nil
}

func bytesIn(v js.Value) []byte {
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b
}

func bytesOut(b []byte) js.Value {
	u := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(u, b)
	return u
}

func entriesIn(v js.Value) ([]bridge.VersionEntry, error) {
	out := make([]bridge.VersionEntry, v.Length())
	for i := range out {
		e := v.Index(i)
		r, err := strconv.ParseUint(e.Get("replicaId").String(), 10, 64)
		if err != nil {
			return nil, err
		}
		c, err := strconv.ParseUint(e.Get("counter").String(), 10, 64)
		if err != nil {
			return nil, err
		}
		out[i] = bridge.VersionEntry{Replica: r, Counter: c}
	}
	return out, nil
}

func newDocument(args []js.Value) any {
	replica, err := strconv.ParseUint(args[0].String(), 10, 64)
	if err != nil {
		return failure(err)
	}
	d, err := bridge.NewDocument(replica)
	if err != nil {
		return failure(err)
	}
	h := nextHandle
	nextHandle++
	documents[h] = d
	return h
}

func free(args []js.Value) any {
	delete(documents, args[0].Int())
	return nil
}

func applyRemote(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	pending, err := d.ApplyRemote(bytesIn(args[1]))
	if err != nil {
		return failure(err)
	}
	return map[string]any{"pending": pending}
}

func applyRemoteAttrs(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	if err := d.ApplyRemoteAttrs(bytesIn(args[1])); err != nil {
		return failure(err)
	}
	return nil
}

func insert(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	b, err := d.Insert(args[1].Int(), args[2].String())
	if err != nil {
		return failure(err)
	}
	return bytesOut(b)
}

func deleteText(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	b, err := d.Delete(args[1].Int(), args[2].Int())
	if err != nil {
		return failure(err)
	}
	return bytesOut(b)
}

func setAttrs(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	o := args[2]
	typ, ok := blocks.ParseType(o.Get("type").String())
	if !ok {
		return map[string]any{"error": "skeincrdt: unknown block type " + o.Get("type").String()}
	}
	want := blocks.Attrs{Type: typ, Indent: uint32(o.Get("indent").Int()), Checked: o.Get("checked").Bool(), Language: o.Get("language").String()}
	b, err := d.SetAttrs(args[1].String(), want)
	if err != nil {
		return failure(err)
	}
	return bytesOut(b)
}

func seedAttrs(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	n, err := d.SeedAttrsFromMarkdown(args[1].String())
	if err != nil {
		return failure(err)
	}
	return n
}

func text(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	return d.Text()
}

func blockList(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	bs, err := d.Blocks()
	if err != nil {
		return failure(err)
	}
	out := make([]any, len(bs))
	for i, b := range bs {
		out[i] = map[string]any{
			"id": b.ID, "type": b.Attrs.Type.String(), "indent": int(b.Attrs.Indent),
			"checked": b.Attrs.Checked, "language": b.Attrs.Language, "text": b.Text, "start": b.Start,
		}
	}
	return out
}

func entriesOut(entries []bridge.VersionEntry) []any {
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = map[string]any{"replicaId": strconv.FormatUint(e.Replica, 10), "counter": strconv.FormatUint(e.Counter, 10)}
	}
	return out
}

func version(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	return entriesOut(d.Version())
}

func lamport(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	return strconv.FormatUint(d.Lamport(), 10)
}

func opsSince(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	entries, err := entriesIn(args[1])
	if err != nil {
		return failure(err)
	}
	return bytesOut(d.OpsSince(entries))
}

func allAttrs(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	return bytesOut(d.AllAttrs())
}

func idAt(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	id, err := d.IDAt(args[1].Int())
	if err != nil {
		return failure(err)
	}
	return id
}

func indexOf(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	index, visible, ok := d.IndexOf(args[1].String())
	return map[string]any{"index": index, "visible": visible, "ok": ok}
}

func markdown(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	md, err := d.Markdown()
	if err != nil {
		return failure(err)
	}
	return md
}

func markdownAt(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	entries, err := entriesIn(args[1])
	if err != nil {
		return failure(err)
	}
	md, err := d.MarkdownAt(entries)
	if err != nil {
		return failure(err)
	}
	return md
}

func exportMapped(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	md, ids, err := d.ExportMapped()
	if err != nil {
		return failure(err)
	}
	list := make([]any, len(ids))
	for i, id := range ids {
		list[i] = id
	}
	return map[string]any{"markdown": md, "ids": list}
}

func nodesOut(nodes []blocks.Node) []any {
	out := make([]any, len(nodes))
	for i, n := range nodes {
		out[i] = map[string]any{
			"kind": n.Kind.String(), "start": n.Start, "end": n.End,
			"contentStart": n.ContentStart, "contentEnd": n.ContentEnd,
			"value": n.Value, "children": nodesOut(n.Children),
		}
	}
	return out
}

func renderInline(args []js.Value) any { return nodesOut(bridge.RenderInline(args[0].String())) }

func describeOps(args []js.Value) any {
	s, err := bridge.DescribeOps(bytesIn(args[0]))
	if err != nil {
		return failure(err)
	}
	return map[string]any{"inserted": s.Inserted, "inserts": s.Inserts, "deletes": s.Deletes}
}

func replaceWithMarkdown(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	ops, attrs, err := d.ReplaceWithMarkdown(args[1].String())
	if err != nil {
		return failure(err)
	}
	return map[string]any{"ops": bytesOut(ops), "attrs": bytesOut(attrs)}
}

func checkInvariants(args []js.Value) any {
	d, bad := doc(args)
	if bad != nil {
		return bad
	}
	if err := d.CheckInvariants(); err != nil {
		return failure(err)
	}
	return nil
}
