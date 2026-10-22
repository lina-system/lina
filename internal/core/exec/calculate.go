// SPDX-License-Identifier: Apache-2.0
package exec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math/big"
	"strings"

	"github.com/thisisjun786/lina/kit/tools"
)

// Calculate accepts bounded decimal arithmetic, never identifiers, functions,
// shell syntax or exponent notation. Rational arithmetic keeps the evidence
// exact and exposes rounding as a separate judgment of the consuming draft.
func Calculate(expression string) (string, error) {
	if len(expression) == 0 || len(expression) > 2048 || strings.IndexFunc(expression, func(r rune) bool { return !strings.ContainsRune("0123456789.+-*/() \t\r\n", r) }) >= 0 {
		return "", tools.ErrInvalid
	}
	node, err := parser.ParseExpr(expression)
	if err != nil {
		return "", tools.ErrInvalid
	}
	budget := 256
	var evaluate func(ast.Expr) (*big.Rat, error)
	evaluate = func(node ast.Expr) (*big.Rat, error) {
		budget--
		if budget < 0 {
			return nil, tools.ErrInvalid
		}
		switch n := node.(type) {
		case *ast.BasicLit:
			if (n.Kind != token.INT && n.Kind != token.FLOAT) || len(n.Value) > 128 {
				return nil, tools.ErrInvalid
			}
			whole, fraction, _ := strings.Cut(n.Value, ".")
			digits, ok := new(big.Int).SetString(whole+fraction, 10)
			if !ok {
				return nil, tools.ErrInvalid
			}
			denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fraction))), nil)
			value := new(big.Rat).SetFrac(digits, denominator)
			if !ok {
				return nil, tools.ErrInvalid
			}
			return value, nil
		case *ast.ParenExpr:
			return evaluate(n.X)
		case *ast.UnaryExpr:
			value, err := evaluate(n.X)
			if err != nil {
				return nil, err
			}
			if n.Op == token.SUB {
				return value.Neg(value), nil
			}
			if n.Op == token.ADD {
				return value, nil
			}
		case *ast.BinaryExpr:
			left, err := evaluate(n.X)
			if err != nil {
				return nil, err
			}
			right, err := evaluate(n.Y)
			if err != nil {
				return nil, err
			}
			// Arithmetic is an explicit subset of Go's much larger token set.
			switch {
			case n.Op == token.ADD:
				return left.Add(left, right), nil
			case n.Op == token.SUB:
				return left.Sub(left, right), nil
			case n.Op == token.MUL:
				return left.Mul(left, right), nil
			case n.Op == token.QUO:
				if right.Sign() != 0 {
					return left.Quo(left, right), nil
				}
			}
		}
		return nil, tools.ErrInvalid
	}
	value, err := evaluate(node)
	if err != nil {
		return "", err
	}
	return value.RatString(), nil
}
