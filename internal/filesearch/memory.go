package filesearch

func estimatedFileRecordBytes(file fileRecord) int64 {
	return int64(len(file.path)+len(file.pathLower)+len(file.pathCompact)+len(file.name)) + 96
}

func estimatedContentIndexBytes(size int64) int64 {
	// Content is stored twice (original + lower-case) plus line offsets and
	// trigram postings. This conservative estimate keeps the process cache under
	// the configured memory budget without expensive exact accounting per file.
	return size*12 + 4096
}

func approximateIndexBytes(idx *Index) int64 {
	var total int64
	for _, file := range idx.files {
		total += estimatedFileRecordBytes(file)
	}
	for _, content := range idx.contents {
		total += int64(len(content.text)+len(content.lowerText)) + int64(len(content.lineStarts))*8 + 96
	}
	for _, posting := range idx.contentGrams {
		total += int64(len(posting))*8 + 16
	}
	return total
}
