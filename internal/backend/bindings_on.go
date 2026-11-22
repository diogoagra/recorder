//go:build bindings

package backend

// generatingBindings: o `wails build`, o `wails dev` e o `wails generate
// module` compilam o app com a tag "bindings" e o executam só para listar os
// métodos do App e escrever os bindings (ver RunWindow).
const generatingBindings = true
