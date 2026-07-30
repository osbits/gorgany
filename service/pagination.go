package service

import (
	"context"
	"fmt"
	"github.com/osbits/gorgany/v2/app/core"
	"github.com/osbits/gorgany/v2/util"
	"math"
	"net/url"
	"strconv"
)

type PaginationService struct{}

func (thiz PaginationService) Pagination(ctx context.Context, offset, limit, total int) string {
	messageCtx, ok := ctx.Value(core.MessageContextKey).(core.IMessageContext)
	if !ok {
		return ""
	}

	totalPages := int(math.Ceil(float64(total) / float64(limit)))
	currentPage := offset/limit + 1

	currentUrl := messageCtx.GetRequest().URL

	items := make([]string, 0)
	if totalPages < 7 {
		for _, i := range util.Range(1, totalPages) {
			items = append(items, thiz.generateItem(currentUrl, i, i == currentPage))
		}
		return thiz.wrapItems(util.Join(items, "\n"), currentUrl, total, limit, currentPage)
	}

	if currentPage < 5 {

		for _, i := range util.Range(1, 6) {
			items = append(items, thiz.generateItem(currentUrl, i, i == currentPage))
		}
		items = append(items, ""+
			"<li class=\"paginate_button page-item disabled\">"+
			"	<a href=\"#\" class=\"page-link\">"+
			"		..."+
			"	</a>"+
			"</li>")

		for _, i := range util.Range(totalPages-1, totalPages) {
			items = append(items, thiz.generateItem(currentUrl, i, i == currentPage))
		}

	} else if currentPage < totalPages-4 {

		for _, i := range util.Range(1, 2) {
			items = append(items, thiz.generateItem(currentUrl, i, i == currentPage))
		}
		items = append(items, ""+
			"<li class=\"paginate_button page-item disabled\">"+
			"	<a href=\"#\" class=\"page-link\">"+
			"		..."+
			"	</a>"+
			"</li>")

		for _, i := range util.Range(currentPage-1, currentPage+1) {
			items = append(items, thiz.generateItem(currentUrl, i, i == currentPage))
		}
		items = append(items, ""+
			"<li class=\"paginate_button page-item disabled\">"+
			"	<a href=\"#\" class=\"page-link\">"+
			"		..."+
			"	</a>"+
			"</li>")

		for _, i := range util.Range(totalPages-1, totalPages) {
			items = append(items, thiz.generateItem(currentUrl, i, i == currentPage))
		}

	} else {

		for _, i := range util.Range(1, 2) {
			items = append(items, thiz.generateItem(currentUrl, i, i == currentPage))
		}
		items = append(items, ""+
			"<li class=\"paginate_button page-item disabled\">"+
			"	<a href=\"#\" class=\"page-link\">"+
			"		..."+
			"	</a>"+
			"</li>")

		for _, i := range util.Range(totalPages-5, totalPages) {
			items = append(items, thiz.generateItem(currentUrl, i, i == currentPage))
		}

	}

	return thiz.wrapItems(util.Join(items, "\n"), currentUrl, total, limit, currentPage)
}

func (thiz PaginationService) wrapItems(items string, url *url.URL, total, limit, currentPage int) string {
	totalPages := int(math.Ceil(float64(total) / float64(limit)))

	currentOffset := (currentPage - 1) * limit

	prevUrlStr := ""
	nextUrlStr := ""

	prevLinkClasses := ""
	nextLinkClasses := ""

	query := url.Query()

	if currentPage > 1 {
		query.Set("page", strconv.Itoa(currentPage-1))
		url.RawQuery = query.Encode()
		prevUrlStr = url.String()
	} else {
		prevLinkClasses += "disabled"
	}

	if currentPage < totalPages {
		query.Set("page", strconv.Itoa(currentPage+1))
		url.RawQuery = query.Encode()
		nextUrlStr = url.String()
	} else {
		nextLinkClasses += "disabled"
	}

	wrappedItems := ""
	if totalPages > 1 {
		wrappedItems = fmt.Sprintf(""+
			"<div class=\"col-sm-12 col-md-7\">"+
			"	<div>"+
			"		<ul class=\"pagination\" style=\"justify-content: flex-end; padding-top: .42em;\">"+
			"			<li class=\"paginate_button page-item %s\">"+
			"				<a href=\"%s\" class=\"page-link\">"+
			"					Previous"+
			"				</a>"+
			"			</li>"+
			"			%s"+
			"			<li class=\"paginate_button page-item next %s\">"+
			"				<a href=\"%s\" class=\"page-link\">"+
			"					Next"+
			"				</a>"+
			"			</li>"+
			"		</ul>"+
			"	</div>"+
			"</div>", prevLinkClasses, prevUrlStr, items, nextLinkClasses, nextUrlStr)
	}

	totalOnPage := currentOffset + limit
	if totalOnPage > total {
		totalOnPage = total
	}

	return fmt.Sprintf(""+
		"<div class=\"row\">"+
		"	<div class=\"col-sm-12 col-md-5\">"+
		"		<div role=\"status\" aria-live=\"polite\" style=\"padding-top: .85em;\">"+
		"			Showing %d to %d of %d entries"+
		"		</div>"+
		"	</div>"+
		"	%s"+
		"</div>", currentOffset+1, totalOnPage, total, wrappedItems)
}

func (thiz PaginationService) generateItem(url *url.URL, page int, active bool) string {
	classes := ""
	if active {
		classes += "active"
	}

	query := url.Query()
	query.Set("page", strconv.Itoa(page))
	url.RawQuery = query.Encode()

	return fmt.Sprintf(""+
		"<li class=\"paginate_button page-item %s\">"+
		"	<a href=\"%s\" class=\"page-link\">"+
		"		%d"+
		"	</a>"+
		"</li>", classes, url.String(), page)
}
