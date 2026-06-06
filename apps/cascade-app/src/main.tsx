/**
 * Purpose: React entry point — mounts the root App component.
 * Inputs:  DOM element #root (created in index.html)
 * Outputs: Rendered React tree
 * Constraints: StrictMode enabled to surface double-render bugs early.
 * SPORT: MASTER-COMPONENTS.md — App root
 */

import React, { Suspense } from "react";
import ReactDOM from "react-dom/client";
import App from "./App";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { LoadingSpinner } from "./components/LoadingSpinner";
import "./styles/globals.css";

const root = document.getElementById("root");
if (!root) throw new Error("Root element #root not found in DOM");

ReactDOM.createRoot(root).render(
  <React.StrictMode>
    <ErrorBoundary>
      <Suspense fallback={<LoadingSpinner />}>
        <App />
      </Suspense>
    </ErrorBoundary>
  </React.StrictMode>
);
