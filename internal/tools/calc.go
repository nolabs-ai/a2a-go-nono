package tools

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// evalExpr evaluates + - * / % ** and parentheses over numeric literals with a
// small recursive-descent parser, so the calculator never runs arbitrary code.
// "//" is floor division, matching the Python demo.
func evalExpr(s string) (float64, error) {
	p := &parser{src: strings.TrimSpace(s)}
	v, err := p.expr()
	if err != nil {
		return 0, err
	}
	p.space()
	if p.pos != len(p.src) {
		return 0, errors.New("unexpected trailing input")
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, errors.New("non-finite result")
	}
	return v, nil
}

type parser struct {
	src string
	pos int
}

func (p *parser) space() {
	for p.pos < len(p.src) && p.src[p.pos] == ' ' {
		p.pos++
	}
}

func (p *parser) accept(tok string) bool {
	p.space()
	if strings.HasPrefix(p.src[p.pos:], tok) {
		p.pos += len(tok)
		return true
	}
	return false
}

func (p *parser) expr() (float64, error) {
	v, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		switch {
		case p.accept("+"):
			r, err := p.term()
			if err != nil {
				return 0, err
			}
			v += r
		case p.accept("-"):
			r, err := p.term()
			if err != nil {
				return 0, err
			}
			v -= r
		default:
			return v, nil
		}
	}
}

func (p *parser) term() (float64, error) {
	v, err := p.unary()
	if err != nil {
		return 0, err
	}
	for {
		p.space()
		switch {
		case p.accept("//"):
			r, err := p.unary()
			if err != nil {
				return 0, err
			}
			v = math.Floor(v / r)
		case p.accept("*"):
			r, err := p.unary()
			if err != nil {
				return 0, err
			}
			v *= r
		case p.accept("/"):
			r, err := p.unary()
			if err != nil {
				return 0, err
			}
			v /= r
		case p.accept("%"):
			r, err := p.unary()
			if err != nil {
				return 0, err
			}
			v = math.Mod(v, r)
		default:
			return v, nil
		}
	}
}

func (p *parser) unary() (float64, error) {
	if p.accept("-") {
		v, err := p.unary()
		return -v, err
	}
	if p.accept("+") {
		return p.unary()
	}
	return p.power()
}

// power is right-associative and binds tighter than unary minus on its left.
func (p *parser) power() (float64, error) {
	base, err := p.atom()
	if err != nil {
		return 0, err
	}
	if p.accept("**") {
		exp, err := p.unary()
		if err != nil {
			return 0, err
		}
		return math.Pow(base, exp), nil
	}
	return base, nil
}

func (p *parser) atom() (float64, error) {
	p.space()
	if p.accept("(") {
		v, err := p.expr()
		if err != nil {
			return 0, err
		}
		if !p.accept(")") {
			return 0, errors.New("missing )")
		}
		return v, nil
	}
	start := p.pos
	for p.pos < len(p.src) && (unicode.IsDigit(rune(p.src[p.pos])) || p.src[p.pos] == '.') {
		p.pos++
	}
	if start == p.pos {
		return 0, errors.New("expected number")
	}
	return strconv.ParseFloat(p.src[start:p.pos], 64)
}
