// Package ui holds the black-box tests of autonomy's UI: the shell and the module pages, driven
// through the HTTP surface a browser uses.
//
// It is a package of its own rather than more files in src/ because of what that buys: a test here
// can only touch what autonomy *exports*, so the UI's contract with the rest of the runtime is what
// gets tested, and a rename inside the runtime breaks this package loudly instead of silently
// following an internal symbol (docs/testing.md).
//
// A directory holding only _test.go files does not build on its own, which is what this file is for.
package ui
