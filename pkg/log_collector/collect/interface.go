package collect

type Collector interface {
	Collect(acc *Accessor) error
}
