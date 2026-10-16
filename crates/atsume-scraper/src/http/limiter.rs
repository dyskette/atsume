//! Spacing out requests to each site.

use std::collections::HashMap;
use std::sync::Mutex;
use std::time::{Duration, Instant};

use crate::budget::{CancelToken, Cancelled};

/// Decides when a request to a host may go out.
///
/// The download workers share one limiter, so a run across hundreds of sites
/// stays polite to each of them.
pub trait Limiter: Send + Sync {
    /// Blocks until a request to `host` may proceed.
    ///
    /// # Errors
    ///
    /// Returns [`Cancelled`] if `cancel` is cancelled while waiting.
    fn wait(&self, host: &str, cancel: &CancelToken) -> Result<(), Cancelled>;
}

/// A limiter that never waits.
pub struct Unlimited;

impl Limiter for Unlimited {
    fn wait(&self, _: &str, cancel: &CancelToken) -> Result<(), Cancelled> {
        cancel.check()
    }
}

/// Limits the rate of requests to each host separately.
///
/// A global limit would either hold the whole queue to the slowest site's
/// pace or overload a small one, so each host gets its own budget.
pub struct HostLimiter {
    /// The time between requests to one host at the steady rate.
    interval: Duration,
    /// How many requests to one host may go out at once before spacing in.
    burst: u32,
    /// When each host's next request would be due at the steady rate.
    due: Mutex<HashMap<String, Instant>>,
}

impl HostLimiter {
    /// Allows `per_second` requests a second to each host, with bursts of up
    /// to `burst`. A rate of zero or less means no limit.
    #[must_use]
    pub fn new(per_second: f64, burst: u32) -> Self {
        let interval = if per_second > 0.0 {
            Duration::from_secs_f64(1.0 / per_second)
        } else {
            Duration::ZERO
        };
        Self {
            interval,
            burst: burst.max(1),
            due: Mutex::new(HashMap::new()),
        }
    }

    /// Books the next request to `host` and returns when it may go out.
    ///
    /// This is the generic cell rate algorithm: each request pushes the host's
    /// due time one interval later, and a request may go out once it is
    /// within `burst` intervals of that time.
    fn reserve(&self, host: &str) -> Instant {
        let now = Instant::now();
        let mut due = self
            .due
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner);
        let next = due.get(host).copied().unwrap_or(now).max(now);
        due.insert(host.to_owned(), next + self.interval);
        let allowance = self.interval * (self.burst - 1);
        next.checked_sub(allowance).unwrap_or(now).max(now)
    }
}

impl Limiter for HostLimiter {
    fn wait(&self, host: &str, cancel: &CancelToken) -> Result<(), Cancelled> {
        let at = self.reserve(host);
        cancel.sleep(at.saturating_duration_since(Instant::now()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn requests_to_one_host_are_spaced_out() {
        let limiter = HostLimiter::new(20.0, 1);
        let cancel = CancelToken::new();
        let start = Instant::now();
        for _ in 0..3 {
            limiter.wait("a.example", &cancel).unwrap();
        }
        // Three requests at 20 a second need two 50 ms gaps.
        assert!(
            start.elapsed() >= Duration::from_millis(95),
            "{:?}",
            start.elapsed()
        );
    }

    #[test]
    fn hosts_do_not_wait_for_each_other_and_bursts_go_at_once() {
        let limiter = HostLimiter::new(1.0, 2);
        let cancel = CancelToken::new();
        let start = Instant::now();
        for host in ["a.example", "a.example", "b.example", "b.example"] {
            limiter.wait(host, &cancel).unwrap();
        }
        assert!(
            start.elapsed() < Duration::from_millis(500),
            "{:?}",
            start.elapsed()
        );
    }

    #[test]
    fn a_cancelled_wait_ends_early() {
        let limiter = HostLimiter::new(0.1, 1);
        let cancel = CancelToken::new();
        limiter.wait("a.example", &cancel).unwrap();
        let canceller = cancel.clone();
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(50));
            canceller.cancel();
        });
        let start = Instant::now();
        assert_eq!(limiter.wait("a.example", &cancel), Err(Cancelled));
        assert!(start.elapsed() < Duration::from_secs(1));
    }
}
