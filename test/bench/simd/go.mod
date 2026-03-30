module simd-bench

go 1.26.0

replace github.com/samber/lo => ../../../lo

replace github.com/samber/lo/exp/simd => ../../../lo/exp/simd

require (
	github.com/samber/lo v0.0.0
	github.com/samber/lo/exp/simd v0.0.0-00010101000000-000000000000
)

require golang.org/x/text v0.22.0 // indirect
