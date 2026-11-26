package curator

// CuratedTopic is the normalized output of Odradek's Curator across all input sources.
type CuratedTopic struct {
	TopicID         int64
	WeeklyThemeID   *int64
	SourceType      string
	SourceURL       string
	Title           string
	Summary         string
	Angle           string
	PatternSlug     *string
	TargetLinkedIn  bool
	TargetSidequest bool
}
