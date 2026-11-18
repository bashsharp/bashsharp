package a

type T struct {
	X int `go:"track"`
}

func (T) GetX() int { return 0 }
