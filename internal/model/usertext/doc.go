// Package usertext holds the user-visible Ukrainian texts the backend produces
// itself (analytics exports, mail dispatch states, defaults of events and
// exercises, setting descriptions). It is the only place of the production Go
// and SQL code, together with the other text modules (error catalog,
// emaildefaults, inappdefaults), where Cyrillic text may be written; the lint
// tools/checkcyrillic keeps it so. A text with values is a function taking the
// values, so no format verbs are scattered at call sites.
package usertext
