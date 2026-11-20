// Copyright 2026 nfltr
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nfltr/relay/client/relaytrust"
	"github.com/nfltr/send/xfer"
)

// docs/v1-contract.md lists exactly what the code has: each command's
// flags (its -h), every environment variable the command reads, the exit
// table (the usage text's codes, named as receive --json names them) and
// the keys of receive --json's line.
func TestContractDoc(t *testing.T) {
	b, err := os.ReadFile("../../docs/v1-contract.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)

	code := map[string][]string{}
	for cmd, run := range map[string]func([]string) int{
		"send":    func(a []string) int { return sendCmd(t.Context(), a) },
		"receive": func(a []string) int { return receiveCmd(t.Context(), a) },
	} {
		for _, f := range flagsOf(t, cmd, run) {
			code[f] = append(code[f], cmd)
		}
	}
	documented := map[string][]string{}
	for _, m := range regexp.MustCompile("(?m)^\\| `--([a-z-]+)[^`]*` \\| ([a-z, ]+) \\|").FindAllStringSubmatch(doc, -1) {
		documented[m[1]] = strings.Split(m[2], ", ")
	}
	sameMap(t, "flags (name: commands)", code, documented)

	var docEnv []string
	for _, m := range regexp.MustCompile("(?m)^\\| `\\$([A-Z0-9_]+)` \\|").FindAllStringSubmatch(doc, -1) {
		docEnv = append(docEnv, m[1])
	}
	same(t, "environment variables", envRead(t), docEnv)

	exits := map[string]string{} // exit -> name, from the doc
	for _, m := range regexp.MustCompile("(?m)^\\| (\\d+) \\|\\s*`?([a-z_0-9]*)`?\\s*\\|").FindAllStringSubmatch(doc, -1) {
		exits[m[1]] = m[2]
	}
	codeExits := map[string]string{"0": ""}
	for n, name := range exitCodes {
		codeExits[strconv.Itoa(n)] = name
	}
	sameMap(t, "exit codes (receive --json's names against the doc)", one(codeExits), one(exits))
	var inUsage []string
	block := usage[strings.Index(usage, "Exit status"):strings.Index(usage, "Flags:")]
	for _, m := range regexp.MustCompile(`(?:^|\s)(\d+) [a-z]`).FindAllStringSubmatch(block, -1) {
		inUsage = append(inUsage, m[1])
	}
	same(t, "exit codes (the usage text against the doc)", inUsage, slices.Collect(maps.Keys(exits)))

	var docJSON []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| (success|failure) \\|").FindAllStringSubmatch(doc, -1) {
		docJSON = append(docJSON, m[1])
	}
	same(t, "receive --json keys", jsonKeys(t), docJSON)

	// A receiver refuses another protocol version: 1.x keeps this one.
	if !strings.Contains(doc, fmt.Sprintf("protocol is version %d (`xfer.Version`", xfer.Version)) || xfer.Version != 4 {
		t.Errorf("xfer.Version is %d: every 1.x release speaks 4 (docs/v1-contract.md \"Compatibility\")", xfer.Version)
	}
}

// flagsOf is cmd's flag names, as its -h (run) prints them.
func flagsOf(t *testing.T, cmd string, run func([]string) int) []string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	run([]string{"-h"})
	os.Stderr = stderr
	_ = w.Close()
	out, _ := io.ReadAll(r)
	var fs []string
	for _, m := range regexp.MustCompile(`(?m)^  -([a-z-]+)`).FindAllStringSubmatch(string(out), -1) {
		fs = append(fs, m[1])
	}
	if len(fs) == 0 {
		t.Fatalf("%s -h listed no flag:\n%s", cmd, out)
	}
	return fs
}

// envRead is every variable the command's code reads: the first argument
// of each os.Getenv, os.LookupEnv and env call outside the tests and the
// trace build.
func envRead(t *testing.T) []string {
	t.Helper()
	named := map[string]string{"relaytrust.PinEnv": relaytrust.PinEnv}
	var vars []string
	for f, af := range sources(t) {
		ast.Inspect(af, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || len(c.Args) == 0 {
				return true
			}
			fn := ""
			switch x := c.Fun.(type) {
			case *ast.Ident:
				fn = x.Name
			case *ast.SelectorExpr:
				if p, ok := x.X.(*ast.Ident); ok {
					fn = p.Name + "." + x.Sel.Name
				}
			}
			if fn != "env" && fn != "os.Getenv" && fn != "os.LookupEnv" {
				return true
			}
			switch a := c.Args[0].(type) {
			case *ast.BasicLit:
				v, _ := strconv.Unquote(a.Value)
				vars = append(vars, v)
			case *ast.SelectorExpr:
				k := a.X.(*ast.Ident).Name + "." + a.Sel.Name
				if named[k] == "" {
					t.Errorf("%s reads the variable %s: name its value in envRead", f, k)
				}
				vars = append(vars, named[k])
			case *ast.Ident: // env's own parameter
			default:
				t.Errorf("%s: an environment read this test cannot name", f)
			}
			return true
		})
	}
	return vars
}

// sources is the command's code, parsed: every package file but the
// tests and the trace build's.
func sources(t *testing.T) map[string]*ast.File {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	out := map[string]*ast.File{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.HasPrefix(f, "trace_") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out[f] = af
	}
	return out
}

// jsonKeys is every key of the map literals the command marshals to JSON
// (receive --json's lines).
func jsonKeys(t *testing.T) []string {
	t.Helper()
	var keys []string
	for _, af := range sources(t) {
		ast.Inspect(af, func(n ast.Node) bool {
			c, ok := n.(*ast.CallExpr)
			if !ok || len(c.Args) != 1 {
				return true
			}
			if s, ok := c.Fun.(*ast.SelectorExpr); !ok || s.Sel.Name != "Marshal" {
				return true
			}
			if lit, ok := c.Args[0].(*ast.CompositeLit); ok {
				for _, e := range lit.Elts {
					if k, ok := e.(*ast.KeyValueExpr).Key.(*ast.BasicLit); ok {
						v, _ := strconv.Unquote(k.Value)
						keys = append(keys, v)
					}
				}
			}
			return true
		})
	}
	return keys
}

func one(m map[string]string) map[string][]string {
	o := map[string][]string{}
	for k, v := range m {
		o[k] = []string{v}
	}
	return o
}

func same(t *testing.T, what string, code, doc []string) {
	t.Helper()
	slices.Sort(code)
	slices.Sort(doc)
	if !slices.Equal(slices.Compact(code), slices.Compact(doc)) {
		t.Errorf("%s: the code has %q, docs/v1-contract.md %q", what, code, doc)
	}
}

func sameMap(t *testing.T, what string, code, doc map[string][]string) {
	t.Helper()
	for k, v := range code {
		slices.Sort(v)
		d := slices.Clone(doc[k])
		slices.Sort(d)
		if !slices.Equal(v, d) {
			t.Errorf("%s: %s is %q in the code, %q in docs/v1-contract.md", what, k, v, d)
		}
	}
	for k := range doc {
		if _, ok := code[k]; !ok {
			t.Errorf("%s: docs/v1-contract.md has %s, the code does not", what, k)
		}
	}
}
