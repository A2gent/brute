# Go request cancellation — synthetic fixture

Synthetic local sample, not a captured public page. Origin URL used for citation tests: https://example.test/go/request-cancellation

## Navigation

Documentation home | Download | Release notes | Community | Jobs | Newsletter | Merchandise | About us | Contact | Site map. Subscribe to receive product news and featured stories. This navigation appears on every page and does not explain request cancellation. Browse articles by date or sign in to save favorites.

## Request cancellation

Create a request with http.NewRequestWithContext(ctx, method, url, body). The context controls the lifetime of the outgoing request and response body. Derive a deadline with context.WithTimeout, call the returned cancel function with defer, and pass the derived context to the request. Cancellation is cooperative: handlers and workers should select on ctx.Done() or check ctx.Err(). Avoid replacing a caller's context with context.Background(), because that breaks propagation.

## Cleanup and error handling

Close response bodies after a successful client.Do call. Release timers by invoking cancel even when the operation finishes before its deadline. Distinguish context.Canceled from context.DeadlineExceeded when reporting an interrupted request. If work runs in a goroutine, ensure every exit path can observe cancellation so the goroutine does not leak. An HTTP client timeout is an additional bound, not a substitute for propagating the caller's context.

## Community sidebar

Our summer meetup welcomes beginners and experts. Members can share photographs, collect stickers, vote for a mascot, or purchase conference tickets. Featured sponsors offer discounted clothing and travel packages. The weekly newsletter contains community announcements and recipes. Follow our social accounts for more event photographs and historical trivia.

## Footer

Copyright synthetic documentation project. Privacy policy | Terms of use | Cookie preferences | Accessibility statement. This sample is maintained solely for offline tests. Its links are reserved test-domain links and should not be fetched. No claim is made that any text is an exact excerpt from Go documentation.
