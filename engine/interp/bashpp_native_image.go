package interp

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
//
// The reviewed synchronous consumers of an original image.Image. The Go Tour
// image exercise hands its own value to pic.ShowImage, which encodes it with
// image/png; both drive the original ColorModel, Bounds and At from dependency
// code and are finished with the value before the call returns. Retained or
// asynchronous image consumers stay refused by the general transport policy.

import "strings"

func synchronousImageCallback(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Receiver != nil {
		return false
	}
	alias, name, ok := strings.Cut(q.Selector, ".")
	if !ok {
		return false
	}
	index := -1
	switch req.Imports[alias] + "." + name {
	case "golang.org/x/tour/pic.ShowImage":
		index = 0
	case "image/png.Encode":
		index = 1
	}
	if index < 0 || index >= len(q.Args) {
		return false
	}
	value := q.Args[index]
	name = strings.TrimPrefix(value.Type, "main.")
	if value.Kind == "pointer" && len(value.Elements) == 1 {
		name = strings.TrimPrefix(value.Elements[0].Type, "main.")
	}
	name = strings.TrimPrefix(name, "*")
	for _, typ := range req.localTypePlan().named(name) {
		// The whole method set must be mirrored: a value serving only part of
		// image.Image is not an image.Image.
		mirrored := map[string]bool{}
		for _, method := range typ.Methods {
			mirrored[method.Name] = true
		}
		return mirrored["ColorModel"] && mirrored["Bounds"] && mirrored["At"]
	}
	return false
}
