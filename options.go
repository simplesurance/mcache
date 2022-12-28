package mcache

// Option allows configuring the behavior of the cache.
type Option func(*settings)

func applyOptions(opt ...Option) *settings {
	ret := settings{}
	for _, o := range opt {
		o(&ret)
	}

	return &ret
}

type settings struct {
	maxMemory int64
}
