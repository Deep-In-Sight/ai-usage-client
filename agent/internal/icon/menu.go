package icon

import "image"

// MenuImage is the picture next to a provider's header in the tray menu:
// its artwork alone, as large as whole multiples allow (design D4). Linux
// and macOS get a 32 px PNG (their menus draw it at 16 px, sharp on HiDPI
// screens); Windows an ICO of TrayIconSizes (the tray loads the entry of
// the small-icon size). nil for a provider without artwork.
func MenuImage(goos string, p Provider) []byte {
	a, ok := arts[p]
	if !ok {
		return nil
	}
	if goos == "windows" {
		var imgs []*image.NRGBA
		for _, n := range TrayIconSizes {
			imgs = append(imgs, menuArt(a, n))
		}
		return ICO(imgs...)
	}
	return PNG(menuArt(a, 32))
}

// menuArt draws a centred on whole cells in a size×size square.
func menuArt(a Art, size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	w, h := a.size()
	m := max(1, min(size/w, size/h))
	a.draw(img, centred(size, w, m), centred(size, h, m), m)
	return img
}
