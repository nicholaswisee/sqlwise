import "vite/modulepreload-polyfill";
import "htmx.org";
import "./style.css";

document.body.addEventListener("htmx:beforeSwap", (event: Event) => {
  const response = event as CustomEvent<{
    xhr: XMLHttpRequest;
    shouldSwap: boolean;
    isError: boolean;
  }>;
  const status = response.detail.xhr.status;
  const contentType = response.detail.xhr.getResponseHeader("Content-Type") ?? "";
  if ((status === 409 || status === 422) && contentType.includes("text/html")) {
    response.detail.shouldSwap = true;
    response.detail.isError = false;
  }
});
