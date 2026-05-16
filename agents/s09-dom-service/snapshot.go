package main

// snapshot.go is the stand-in for the real CDP DOMSnapshot pipeline.
// Upstream (`browser_use/dom/service.py::_get_all_trees`) does
// roughly this, in ~250 lines:
//
//   1. Wait for document.readyState
//   2. Collect iframe scroll positions via Runtime.evaluate
//   3. Detect JS click listeners (getEventListeners + DOM.describeNode)
//   4. Call DOMSnapshot.captureSnapshot (paint order, computed styles,
//      DOM rects, all in one round-trip)
//   5. Call DOM.getDocument and Accessibility.getFullAXTree per frame
//   6. Merge everything into an EnhancedDOMTreeNode tree
//
// In s09 we replace that whole subsystem with a function pointer of
// type SnapshotFunc. The service calls it on every cache miss; tests
// supply a recording stub; the demo supplies a "page A vs page B"
// stub so a navigation actually shows different DOM.
//
// Why a function type and not an interface? Because everything else
// in s09 is data (Cache, EventBus, SerializedState), and SnapshotFunc
// is the *one* place we expose an injection seam. A function type
// makes it trivial for tests to count calls (`count++`) without
// declaring a 1-method interface + stub struct.

// SnapshotFunc is the contract every snapshot source must satisfy.
// Returns the root of the DOM tree at the moment of the call; the
// service is responsible for caching the result. The url argument
// lets the implementation branch on which page is "loaded" — useful
// for testing navigation invalidation, where pageA and pageB must
// produce *different* trees so a stale cache is visible.
//
// Errors: in production a snapshot can fail (page crashed, CDP
// disconnected, frame detached). We surface that as an error so the
// service can decide whether to return the stale cache or propagate
// — see DOMService.Get for the policy.
type SnapshotFunc func(url string) (*DOMNode, error)

// NewStubSnapshot returns a SnapshotFunc that produces one of two
// hand-crafted DOM trees depending on the URL. The two trees are
// structurally different (different tags, different texts) so the
// "cache invalidated on navigation" test can prove the new tree
// reached the cache.
//
// We also return a `*int` so the caller can observe how many times
// the stub was invoked. The whole point of the cache test is to
// assert "Get called twice → Snapshot called once"; a counter is the
// most readable oracle.
//
// pageA == https://a.example.com — a 2-button login form.
// pageB == any other URL          — a 3-link nav bar.
func NewStubSnapshot() (SnapshotFunc, *int) {
	calls := 0
	fn := func(url string) (*DOMNode, error) {
		calls++
		if url == "https://a.example.com" {
			return pageATree(), nil
		}
		return pageBTree(), nil
	}
	return fn, &calls
}

// pageATree is a 2-button login form rendered as a 4-node tree:
//
//     <body>
//       └─ <form> "login"
//           ├─ <button> "submit"   [BackendNodeID=10, bbox=10,10,80,30, visible]
//           └─ <button> "cancel"   [BackendNodeID=11, bbox=10,50,80,30, visible]
//
// The form node itself is non-visible (it's a layout container);
// only the two buttons make it to the serialized output. The tree
// is *flat* enough that the iframe-depth test below has to use a
// separate fixture to exercise nesting.
func pageATree() *DOMNode {
	return &DOMNode{
		Tag:     "body",
		Visible: false,
		Children: []*DOMNode{
			{
				Tag:     "form",
				Text:    "login",
				Visible: false,
				Children: []*DOMNode{
					{
						BackendNodeID: 10,
						Tag:           "button",
						Text:          "submit",
						BBox:          [4]int{10, 10, 80, 30},
						Visible:       true,
					},
					{
						BackendNodeID: 11,
						Tag:           "button",
						Text:          "cancel",
						BBox:          [4]int{10, 50, 80, 30},
						Visible:       true,
					},
				},
			},
		},
	}
}

// pageBTree is the post-navigation tree: a 3-link nav bar. Different
// tags ("a" instead of "button"), different texts ("home"/"about"/
// "contact"), different BackendNodeIDs (20/21/22) — three signals
// that the test can use to distinguish from pageA.
func pageBTree() *DOMNode {
	return &DOMNode{
		Tag:     "body",
		Visible: false,
		Children: []*DOMNode{
			{
				Tag:     "nav",
				Visible: false,
				Children: []*DOMNode{
					{BackendNodeID: 20, Tag: "a", Text: "home", BBox: [4]int{0, 0, 60, 20}, Visible: true},
					{BackendNodeID: 21, Tag: "a", Text: "about", BBox: [4]int{60, 0, 60, 20}, Visible: true},
					{BackendNodeID: 22, Tag: "a", Text: "contact", BBox: [4]int{120, 0, 60, 20}, Visible: true},
				},
			},
		},
	}
}
