// Package rewrite is middleware for rewriting requests internally to
// a different path.
package rewrite

import (
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mholt/caddy/middleware"
)

// Rewrite is middleware to rewrite request locations internally before being handled.
type Rewrite struct {
	Next    middleware.Handler
	FileSys http.FileSystem
	Rules   []Rule
}

// ServeHTTP implements the middleware.Handler interface.
func (rw Rewrite) ServeHTTP(w http.ResponseWriter, r *http.Request) (int, error) {
	for _, rule := range rw.Rules {
		if ok := rule.Rewrite(rw.FileSys, r); ok {
			break
		}
	}
	return rw.Next.ServeHTTP(w, r)
}

// Rule describes an internal location rewrite rule.
type Rule interface {
	// Rewrite rewrites the internal location of the current request.
	Rewrite(http.FileSystem, *http.Request) bool
}

// SimpleRule is a simple rewrite rule.
type SimpleRule struct {
	From, To string
}

// NewSimpleRule creates a new Simple Rule
func NewSimpleRule(from, to string) SimpleRule {
	return SimpleRule{from, to}
}

// Rewrite rewrites the internal location of the current request.
func (s SimpleRule) Rewrite(fs http.FileSystem, r *http.Request) bool {
	if s.From == r.URL.Path {
		// take note of this rewrite for internal use by fastcgi
		// all we need is the URI, not full URL
		r.Header.Set(headerFieldName, r.URL.RequestURI())

		// attempt rewrite
		return To(fs, r, s.To, newReplacer(r))
	}
	return false
}

// ComplexRule is a rewrite rule based on a regular expression
type ComplexRule struct {
	// Path base. Request to this path and subpaths will be rewritten
	Base string

	// Path to rewrite to
	To string

	// Extensions to filter by
	Exts []string

	// Rewrite conditions
	Ifs []If

	*regexp.Regexp
}

// NewRegexpRule creates a new RegexpRule. It returns an error if regexp
// pattern (pattern) or extensions (ext) are invalid.
func NewComplexRule(base, pattern, to string, ext []string, ifs []If) (*ComplexRule, error) {
	// validate regexp if present
	var r *regexp.Regexp
	if pattern != "" {
		var err error
		r, err = regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
	}

	// validate extensions if present
	for _, v := range ext {
		if len(v) < 2 || (len(v) < 3 && v[0] == '!') {
			// check if no extension is specified
			if v != "/" && v != "!/" {
				return nil, fmt.Errorf("invalid extension %v", v)
			}
		}
	}

	return &ComplexRule{
		Base:   base,
		To:     to,
		Exts:   ext,
		Ifs:    ifs,
		Regexp: r,
	}, nil
}

// Rewrite rewrites the internal location of the current request.
func (r *ComplexRule) Rewrite(fs http.FileSystem, req *http.Request) bool {
	rPath := req.URL.Path
	replacer := newReplacer(req)

	// validate base
	if !middleware.Path(rPath).Matches(r.Base) {
		return false
	}

	// validate extensions
	if !r.matchExt(rPath) {
		return false
	}

	// include trailing slash in regexp if present
	start := len(r.Base)
	if strings.HasSuffix(r.Base, "/") {
		start--
	}

	// validate regexp if present
	if r.Regexp != nil {
		matches := r.FindStringSubmatch(rPath[start:])
		switch len(matches) {
		case 0:
			// no match
			return false
		default:
			// set regexp match variables {1}, {2} ...
			for i := 1; i < len(matches); i++ {
				replacer.Set(fmt.Sprint(i), matches[i])
			}
		}
	}

	// validate rewrite conditions
	for _, i := range r.Ifs {
		if !i.True(req) {
			return false
		}
	}

	// attempt rewrite
	return To(fs, req, r.To, replacer)
}

// matchExt matches rPath against registered file extensions.
// Returns true if a match is found and false otherwise.
func (r *ComplexRule) matchExt(rPath string) bool {
	f := filepath.Base(rPath)
	ext := path.Ext(f)
	if ext == "" {
		ext = "/"
	}

	mustUse := false
	for _, v := range r.Exts {
		use := true
		if v[0] == '!' {
			use = false
			v = v[1:]
		}

		if use {
			mustUse = true
		}

		if ext == v {
			return use
		}
	}

	if mustUse {
		return false
	}
	return true
}

// When a rewrite is performed, this header is added to the request
// and is for internal use only, specifically the fastcgi middleware.
// It contains the original request URI before the rewrite.
const headerFieldName = "Caddy-Rewrite-Original-URI"
