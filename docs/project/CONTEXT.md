# WOPR project context

This file defines the terms that public WOPR documentation and implementation use.

## WOPR

WOPR is the terminal coding agent in this repository, written in Go. The executable is `wopr`.

## Resource

A Resource contributes one skill, prompt template, theme, or context file.

## Evidence

Evidence is a reproducible artifact that supports a project claim. Scanner output is evidence input. It is not a validated inventory, license conclusion, vulnerability disposition, or compliance approval by itself.

## Transcript

A Transcript is normalized, ordered model context. It carries typed system, user, assistant, and tool-result entries in their original order.

## Provider

A Provider consumes a normalized Transcript and produces one Event Stream.

## Event Stream

An Event Stream yields ordered model events and one terminal assistant result. The caller context owns cancellation.

## Model Runtime

A Model Runtime owns mode-independent model lookup, authentication, completion, and streaming. TUI, print, JSON, and RPC use the same Model Runtime.

## Main Screen

The Main Screen renders transcript and active input into the visible terminal region. Ordinary input changes update only the required visible rows and preserve terminal scrollback unless a change above the viewport requires a recovery redraw.
