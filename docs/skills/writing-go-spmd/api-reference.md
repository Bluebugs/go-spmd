# Go SPMD API Reference

## Varying Type

```go
import "lanes"

// Compiler magic -- not regular generics. Intercepted before generic instantiation.
type Varying[T any] struct{ _ [0]T }
```

- Uniform values are regular Go types (no annotation needed)
- `Varying[T]` values differ across SIMD lanes
- Uniform is automatically broadcast to varying when needed
- Varying CANNOT be assigned to uniform (use `reduce` package)

## Lane Count Table (WASM SIMD128)

| Go Type | `lanes.Count` | LLVM Type | Bits/Element |
|---------|---------------|-----------|-------------|
| `bool` | 16 | `<16 x i1>` | 1 |
| `int8` / `uint8` / `byte` | 16 | `<16 x i8>` | 8 |
| `int16` / `uint16` | 8 | `<8 x i16>` | 16 |
| `int32` / `uint32` / `float32` | 4 | `<4 x i32>` / `<4 x f32>` | 32 |
| `int64` / `uint64` / `float64` | 2 | `<2 x i64>` / `<2 x f64>` | 64 |
| `int` / `uint` (WASM=32-bit) | 4 | `<4 x i32>` | 32 |

Formula: `lanes = 128 / bitwidth`. **`Varying[bool]` = 16 lanes, not 4.**

## lanes Package (12 Functions)

Source: `go/src/lanes/lanes.go`

| Function | Signature | Status | Notes |
|----------|-----------|--------|-------|
| `Index` | `func Index() Varying[int]` | Done | Lane index [0..Count-1]. Only in `go for` or SPMD func |
| `Count` | `func Count[T any](v Varying[T]) int` | Done | Compile-time constant. See lane count table |
| `Broadcast` | `func Broadcast[T any](v Varying[T], lane int) Varying[T]` | Done | Copy one lane's value to all lanes |
| `From` | `func From[T any](data []T) Varying[T]` | Done | Load uniform slice as varying |
| `ShiftLeft` | `func ShiftLeft[T integer](v Varying[T], shift Varying[T]) Varying[T]` | Done | Cross-lane left shift, fill with zero |
| `ShiftRight` | `func ShiftRight[T integer](v Varying[T], shift Varying[T]) Varying[T]` | Done | Cross-lane right shift, fill with zero |
| `Rotate` | `func Rotate[T any](v Varying[T], offset int) Varying[T]` | **Deferred** | Full-width circular rotation |
| `Swizzle` | `func Swizzle[T any](v Varying[T], indices Varying[int]) Varying[T]` | **Deferred** | Full-width arbitrary permutation |
| `RotateWithin` | `func RotateWithin[T any](v Varying[T], offset int, groupSize int) Varying[T]` | Done | Rotate within groups of N lanes |
| `ShiftLeftWithin` | `func ShiftLeftWithin[T any](v Varying[T], amount int, groupSize int) Varying[T]` | Done | Shift left within groups, fill zero |
| `ShiftRightWithin` | `func ShiftRightWithin[T any](v Varying[T], amount int, groupSize int) Varying[T]` | Done | Shift right within groups, fill zero |
| `SwizzleWithin` | `func SwizzleWithin[T any](v Varying[T], indices Varying[int], groupSize int) Varying[T]` | **Deferred** | Permute within groups (variable indices) |

### Type Constraint

```go
type integer interface {
    ~int | ~int8 | ~int16 | ~int32 | ~int64 |
    ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}
```

### *Within Operations

- `groupSize` must be a compile-time constant that evenly divides the lane count
- Enables portable algorithms independent of hardware SIMD width
- Example: base64 uses `groupSize=4` for 4:3 byte transformation; works on 4-lane, 8-lane, or 16-lane hardware

## reduce Package (13 Functions)

Source: `go/src/reduce/reduce.go`. All are compiler builtins (stub implementations panic at runtime).

| Function | Signature | Returns | Notes |
|----------|-----------|---------|-------|
| `Add` | `func Add[T numeric](data Varying[T]) T` | Sum of all lanes | Horizontal add |
| `Mul` | `func Mul[T numeric](data Varying[T]) T` | Product of all lanes | Horizontal multiply |
| `Max` | `func Max[T ordered](data Varying[T]) T` | Maximum value | |
| `Min` | `func Min[T ordered](data Varying[T]) T` | Minimum value | |
| `All` | `func All(data Varying[bool]) bool` | True if ALL lanes true | Use for uniform early exit |
| `Any` | `func Any(data Varying[bool]) bool` | True if ANY lane true | Use for uniform early exit |
| `And` | `func And[T integer](data Varying[T]) T` | Bitwise AND all lanes | |
| `Or` | `func Or[T integer](data Varying[T]) T` | Bitwise OR all lanes | |
| `Xor` | `func Xor[T integer](data Varying[T]) T` | Bitwise XOR all lanes | |
| `From` | `func From[T any](data Varying[T]) []T` | Uniform slice | Convert varying to array |
| `Count` | `func Count(data Varying[bool]) int` | Number of true lanes | |
| `FindFirstSet` | `func FindFirstSet(data Varying[bool]) int` | Index of first true lane | 0-based, lane-relative |
| `Mask` | `func Mask(data Varying[bool]) int` | Bitmask (bit N = lane N) | For extracting lane pattern |

### Type Constraints

```go
type numeric interface {
    ~int | ~int8 | ~int16 | ~int32 | ~int64 |
    ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr |
    ~float32 | ~float64
}

type ordered interface {
    numeric | ~string
}

type integer interface {
    ~int | ~int8 | ~int16 | ~int32 | ~int64 |
    ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}
```

### Key Reduction Patterns

```go
// Convert varying condition to uniform for early exit
if reduce.Any(cond) { return ... }    // Exit when ANY lane matches
if reduce.All(escaped) { break }       // Exit when ALL lanes done

// Extract first error from varying
errors := reduce.From(errVarying)
firstErr := errors[reduce.FindFirstSet(hasError)]

// Create bitmask from boolean varying
mask := reduce.Mask(isDot)   // bit N = lane N's bool value
```

## Printf Integration

`fmt.Printf` with `%v` automatically converts `Varying[T]` to `[]T` via `reduce.From()`:

```go
go for _, v := range data {
    fmt.Printf("Values: %v\n", v)       // Auto-converts to []T
    fmt.Printf("Doubled: %v\n", v*2)    // Works with expressions too
}
```

## Type Conversion Rules

| From | To | Allowed? | Notes |
|------|----|----------|-------|
| `int` (uniform) | `Varying[int]` | Yes | Implicit broadcast |
| `Varying[int]` | `int` (uniform) | No | Use `reduce.Add/From/etc.` |
| `Varying[int32]` | `Varying[int8]` | Yes | Downcast (truncates) |
| `Varying[int8]` | `Varying[int32]` | No | Upcast exceeds 128 bits |
| `Varying[T]` | `interface{}`/`any` | Yes | Auto-conversion |
| `Varying[T]` | map key | No | Varying map keys forbidden |
| `Varying[T]` | channel element | Yes | `chan Varying[int]` OK |
