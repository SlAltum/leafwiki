package embed

import (
	"database/sql"
	"sort"
)

// ChunkHit is one passage matched by a vector query.
type ChunkHit struct {
	PageID      string
	ChunkNo     int
	Heading     string
	HeadingPath string
	Anchor      string
	Score       float64
}

// SearchVector returns the passages of buildID closest to query, one per page
// (its best passage), ordered by cosine similarity.
//
// The scan is linear over the build's chunks: the index is small enough that an
// approximate structure would only add a failure mode (a few tens of thousands
// of chunks is what an instance holds).
func (s *Store) SearchVector(buildID string, query []float32, minSimilarity float64, limit int) ([]ChunkHit, error) {
	if buildID == "" || len(query) == 0 {
		return nil, nil
	}
	normalized := normalizeVec(query)

	var hits []ChunkHit
	err := s.withDBRead(func(db *sql.DB) error {
		rows, err := db.Query(`SELECT page_id, chunk_no, heading, heading_path, anchor, vec
			FROM chunks WHERE build_id = ?`, buildID)
		if err != nil {
			return err
		}
		defer func() {
			_ = rows.Close()
		}()

		best := make(map[string]ChunkHit)
		for rows.Next() {
			var hit ChunkHit
			var raw []byte
			if err := rows.Scan(&hit.PageID, &hit.ChunkNo, &hit.Heading, &hit.HeadingPath, &hit.Anchor, &raw); err != nil {
				return err
			}
			vec := decodeVec(raw)
			if len(vec) != len(normalized) {
				// A build made by another model cannot answer this query.
				continue
			}
			score := dotProduct(normalized, vec)
			if score < minSimilarity {
				continue
			}
			hit.Score = score
			if previous, ok := best[hit.PageID]; !ok || score > previous.Score {
				best[hit.PageID] = hit
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}

		hits = make([]ChunkHit, 0, len(best))
		for _, hit := range best {
			hits = append(hits, hit)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].PageID < hits[j].PageID
		}
		return hits[i].Score > hits[j].Score
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// dotProduct is the cosine of two normalized vectors.
func dotProduct(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}
