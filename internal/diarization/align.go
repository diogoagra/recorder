package diarization

// Segment é um trecho do áudio atribuído a uma pessoa.
type Segment struct {
	Start   float64 // segundos desde o início do áudio
	End     float64
	Speaker string
}

// SpeakerAt devolve quem mais falou no intervalo [start, end] segundo segs.
// Se ninguém falou nele, fica com o trecho mais próximo, já que a
// segmentação às vezes corta o começo ou o fim das frases.
func SpeakerAt(segs []Segment, start, end float64) string {
	talk := map[string]float64{}
	best, bestTalk := "", 0.0
	for _, s := range segs {
		if s.Speaker == "" {
			continue
		}
		overlap := min(end, s.End) - max(start, s.Start)
		if overlap <= 0 {
			continue
		}
		talk[s.Speaker] += overlap
		if talk[s.Speaker] > bestTalk {
			best, bestTalk = s.Speaker, talk[s.Speaker]
		}
	}
	if best != "" {
		return best
	}

	nearestGap := -1.0
	for _, s := range segs {
		if s.Speaker == "" {
			continue
		}
		gap := max(s.Start-end, start-s.End)
		if nearestGap < 0 || gap < nearestGap {
			best, nearestGap = s.Speaker, gap
		}
	}
	return best
}
