package main

import (
	"fmt"
	"strconv"
	"strings"
)

type sessionLocator struct {
	Node string
	ID   *uint32
	Name string
}

func (l sessionLocator) isRemote() bool { return l.Node != "" }

func parseSessionLocator(arg string) (sessionLocator, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return sessionLocator{}, fmt.Errorf("session locator is empty")
	}

	if strings.Count(arg, ":") > 1 {
		return sessionLocator{}, fmt.Errorf("invalid session locator %q", arg)
	}

	if node, rest, ok := strings.Cut(arg, ":"); ok {
		node = strings.TrimSpace(node)
		rest = strings.TrimSpace(rest)
		if node == "" || rest == "" {
			return sessionLocator{}, fmt.Errorf("invalid session locator %q", arg)
		}
		loc, err := parseSessionTarget(rest)
		if err != nil {
			return sessionLocator{}, err
		}
		loc.Node = node
		return loc, nil
	}

	return parseSessionTarget(arg)
}

func parseSessionTarget(arg string) (sessionLocator, error) {
	name := strings.TrimPrefix(strings.TrimSpace(arg), "@")
	if name == "" {
		return sessionLocator{}, fmt.Errorf("invalid session locator %q", arg)
	}
	if parsed, err := strconv.ParseUint(name, 10, 32); err == nil {
		id := uint32(parsed)
		return sessionLocator{ID: &id}, nil
	}
	return sessionLocator{Name: name}, nil
}
