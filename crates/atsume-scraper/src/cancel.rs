//! Cancelling a scrape from another thread.

use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

/// How often a blocking wait checks whether the scrape was cancelled.
pub(crate) const CANCEL_POLL_INTERVAL: Duration = Duration::from_millis(20);

/// Cancels a scrape from any thread.
///
/// Clones share one token. Once it is cancelled, no new call into the module
/// starts, a running call stops at the CPU budget's next check, and a request
/// or wait in progress is abandoned.
#[derive(Clone, Debug, Default)]
pub struct CancelToken(Arc<AtomicBool>);

/// The scrape was cancelled while waiting.
#[derive(Clone, Copy, Debug, PartialEq, Eq, thiserror::Error)]
#[error("scrape cancelled")]
pub struct Cancelled;

impl CancelToken {
    /// Returns a token that has not been cancelled.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Cancels the scrape. Calling it again has no further effect.
    pub fn cancel(&self) {
        self.0.store(true, Ordering::Relaxed);
    }

    /// Reports whether [`cancel`](Self::cancel) has been called.
    #[must_use]
    pub fn is_cancelled(&self) -> bool {
        self.0.load(Ordering::Relaxed)
    }

    /// Fails with [`Cancelled`] once the token is cancelled.
    ///
    /// # Errors
    ///
    /// Returns [`Cancelled`] if [`cancel`](Self::cancel) has been called.
    pub fn check(&self) -> Result<(), Cancelled> {
        if self.is_cancelled() {
            Err(Cancelled)
        } else {
            Ok(())
        }
    }

    /// Sleeps for `duration`, waking early if the token is cancelled.
    ///
    /// # Errors
    ///
    /// Returns [`Cancelled`] if the token is cancelled before or during the
    /// sleep.
    pub fn sleep(&self, duration: Duration) -> Result<(), Cancelled> {
        let deadline = Instant::now() + duration;
        loop {
            self.check()?;
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return Ok(());
            }
            std::thread::sleep(left.min(CANCEL_POLL_INTERVAL));
        }
    }
}
