package handlers

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

func parsePage(c *gin.Context, defaultLimit int) (page, limit, offset int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit = defaultLimit
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
			if limit > 200 {
				limit = 200
			}
		}
	}
	offset = (page - 1) * limit
	return
}

func pageCount(total int64, limit int) int {
	if limit <= 0 {
		return 1
	}
	n := int((total + int64(limit) - 1) / int64(limit))
	if n < 1 {
		return 1
	}
	return n
}

func showingRange(total int64, offset, count int) (from, to int) {
	if total == 0 || count == 0 {
		return 0, 0
	}
	from = offset + 1
	to = offset + count
	return
}
